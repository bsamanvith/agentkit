---
id: T030
title: "pkg/runner: run agents with persistence"
epic: E05
status: todo
depends_on: [T019, T022, T028]
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
- Implements T024's `TranscriptSaver`, so `harness.Step` persists through the store.

## Acceptance criteria

- [ ] A run of N turns writes 1 prompt plus N turn entries, and the stored context rebuilds the exact `Result` transcript.
- [ ] A crash after turn k (simulated by cancelling the consumer at a fixed event) leaves k turns stored; `Resume` makes the same remaining requests as an uninterrupted run.
- [ ] Two runners on one conversation: one wins the lease, the other gets `ErrLeased`; a stale writer that loses a sequence race stops without overwriting.
- [ ] The cache returns the same context as a cold load after every append; it never serves entries from another tenant or conversation.
- [ ] Invariants named and checked after every run (§2.6): stored entries are a prefix-consistent copy of the harness's turn events; the stored context round-trips to the harness transcript; the lease is released on every exit path.
- [ ] Seeded simulation of crashes, retries and concurrent runners against `store/memory`, plus a fuzz target over the seed. Mutation list recorded.

## Notes

- archtest: `pkg/runner` may import `harness`, `store`, `llm`, `tool`, `tenancy`, `clock`.
