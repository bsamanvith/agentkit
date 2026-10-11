---
id: T030
title: "pkg/runner: run agents with persistence"
epic: E05
status: todo
depends_on: [T019, T022, T024, T028, T029]
---

# T030: `pkg/runner`

## Context

The component that runs agents ([E05 decisions](E05-store-runner.md)): the only place that knows both the harness and the store. It turns harness events into store entries and stored context back into a harness `Transcript`. Contract: T022 "Contract for the store layer".

## Scope

- `runner.New(store, harnessFactory, opts...)`; `Run(ctx, convID, message)` and `Resume(ctx, runID)`, both streaming the harness's events.
- A run:
  1. claims the run lease;
  2. loads `Context` (from the cache when warm);
  3. writes the prompt entry first;
  4. writes one `turn` entry per `EventTurnDone` (messages plus the `TurnRecord` as `Record`);
  5. finishes the run record with status, usage and error.

  A crash loses at most the turn in flight.
- Sequence numbers come from the context's position; a conflict means another writer: stop, reload and report, never overwrite.
- In-process context cache per conversation, keyed by the log position it was built from, appended to and never invalidated (the log only grows); bounded by an LRU.
- `Resume`: a run whose stored transcript ends on a user message continues with `harness.Input{Transcript}` and no message.
- Implements `agentstep.TranscriptSaver` (`Load(ctx, runID, path)`, `Append(ctx, runID, path, msgs)`) on top of `ConversationStore`, so agent steps in flows persist through the store, and runs `agentsteptest.RunSaver` against it.
- Read T028's "For T030 (runner)" Notes first: sequence assignment, the cache update, leases, transitions (not idempotent), errors.
- **Decided (planner), when stopped runs continue.** The runner owns the policy; `RunStore` (T028) holds the statuses.
  - **Recovery after a crash or deploy:** `runner.Recover(ctx)` lists runs across tenants with `store.Admin.ListResumableAll` (T029), and for each puts its tenant in `ctx`, then claims each (the lease stops two replicas resuming the same run) and resumes it, with bounded concurrency. `agentkit serve` runs it at startup and then periodically once the runner is a component. A recovered agent step continues from its last saved turn.
  - **Retry policy:** a run that fails with an error matching `llm.IsRetryable` becomes `failed` + retryable with `NextAttemptAt` = now + exponential backoff with jitter (from the injected clock), up to a maximum number of attempts, then `failed` for good. `ErrBudgetExceeded`, `ErrRefused`, `ErrMaxTokens`, `ErrShapeChanged` and invalid-input errors are not retried automatically; an operator can resume them explicitly (for example with a bigger budget). `llm.ErrContextTooLong` goes to compaction (T031).
  - **Approvals:** a flow that suspends sets the run to `suspended` with its pending paths and releases the lease. `runner.Decide(ctx, runID, path, flow.Decision)` records the decision and resumes the run; a rejection makes it `rejected` (final).
  - **One unfinished run per conversation:** `Run(ctx, convID, message)` fails with `ErrRunUnfinished` (carrying the run ID) while the conversation has an unfinished run; the caller resumes it or calls `runner.Cancel(ctx, runID)`. `Cancel` makes the run `canceled`; if the conversation ends on a user message (the run stopped before the model answered), it appends a marker assistant message saying the run was canceled, so the next run sends a valid, alternating conversation. Nothing is edited.
- **Decided (T032 handoff), flow shape changes:**
  - `flow.Resume` returns `ErrShapeChanged` before anything runs and writes nothing. The runner reports it as a **distinct, non-retryable run failure**, releases the lease, and leaves the run resumable (by the old code, or by an operator decision).
  - `flow.WithAllowShapeChange` is passed only on an **explicit operator decision** (a per-resume option), never by default.
  - **A completed run is never resumed.** The runner checks the run's status in `RunStore` first; for a finished run it reads the final state from the checkpoint (`Checkpointer.Load`) and returns it, without calling `Resume`. So a changed step tree can never make a completed run fail.
  - `flow.EventWarning` is surfaced in the run's events (`errors.Is` `ErrNoShape`: no fingerprint or one of another version; `ErrShapeChanged`: the opt-in was used). Event observers must tolerate event kinds they do not know.
  - Custom leaf steps (`agentstep`) count in the fingerprint by **name only**: changing an agent's model or prompt does not change it. Documented in the runner's package docs; operators who want such changes detected put a version in the step name.
- **Decided (T024 handoff):**
  - **Saved transcripts are not deleted** when a run completes or is rejected. They are the conversation log (append-only, used for history, audit and billing). Removing old data is a separate retention policy per tenant, planned later; it never runs as a side effect of a run finishing.
  - **Agent step output holds a reference, not the transcript.** With a store-backed saver, `agentstep.Output` keeps the answer plus a reference (conversation ID and the sequence number it ends at), so flow checkpoints stay small however many turns an agent takes. The memory and file savers keep the current behavior. This changes `pkg/agentstep`: owned by this task, keeping its tests and `RunSaver` green.

## Acceptance criteria

- [ ] A run of N turns writes 1 prompt plus N turn entries, and the stored context rebuilds the exact `Result` transcript.
- [ ] A crash after turn k (simulated by cancelling the consumer at a fixed event) leaves k turns stored; `Resume` makes the same remaining requests as an uninterrupted run.
- [ ] Two runners on one conversation: one wins the lease, the other gets `ErrLeased`; a stale writer that loses a sequence race stops without overwriting.
- [ ] The cache returns the same context as a cold load after every append; it never serves entries from another tenant or conversation.
- [ ] Invariants named and checked after every run (§2.6): stored entries are a prefix-consistent copy of the harness's turn events; the stored context round-trips to the harness transcript; the lease is released on every exit path.
- [ ] Recovery: runs killed at fixed points (before the first turn, between turns, mid-flow, with two replicas running `Recover` at once) are each resumed exactly once and finish with the same transcript as an uninterrupted run.
- [ ] Retry: a model that fails retryably twice then succeeds is retried with growing delays (checked with the fake clock) and completes; one that keeps failing stops after the maximum attempts as `failed`; a non-retryable error is never retried automatically.
- [ ] Approvals: a suspended run is listed by `ListSuspended`; `Decide` with approval resumes it to completion; a rejection makes it `rejected` and it is never resumed.
- [ ] A new message on a conversation with an unfinished run fails with `ErrRunUnfinished`; after `Cancel` (including a run canceled before the model answered) the next message runs and its first request is a valid, alternating conversation.
- [ ] Resuming against a changed step tree fails with a distinct, non-retryable error, writes nothing, releases the lease, and a later resume with the original tree succeeds; with the explicit operator option it proceeds.
- [ ] A completed run is returned from its checkpoint without `Resume`, even when the current step tree differs.
- [ ] `flow.EventWarning` reaches the run's event stream; an observer given an unknown event kind does not fail.
- [ ] The store-backed saver passes `agentsteptest.RunSaver`; an agent step in a flow resumes from it after a simulated crash.
- [ ] With the store-backed saver, a flow checkpoint after an N-turn agent step holds a reference, not the transcript (its size does not grow with N), and the transcript reads back from the store byte-identical.
- [ ] Seeded simulation of crashes, retries and concurrent runners against `store/memory`, plus a fuzz target over the seed. Mutation list recorded.

## Notes

- archtest: `pkg/runner` may import `harness`, `store`, `llm`, `tool`, `tenancy`, `clock`.
