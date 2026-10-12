---
id: T030
title: "pkg/runner: run agents with persistence"
epic: E05
status: done
depends_on: [T019, T022, T024, T028, T038]
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
  - **Recovery after a crash or deploy:** `runner.Recover(ctx)` lists runs across tenants with `store.Admin.ListResumableAll` (T038), and for each puts its tenant in `ctx`, then claims each (the lease stops two replicas resuming the same run) and resumes it, with bounded concurrency. `agentkit serve` runs it at startup and then periodically once the runner is a component. A recovered agent step continues from its last saved turn.
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

- [x] A run of N turns writes 1 prompt plus N turn entries, and the stored context rebuilds the exact `Result` transcript. (`TestRunStoresThePromptAndEveryTurn`: 0, 1 and 4 tool turns plus a follow-up; invariants 1 and 2 after every test)
- [x] A crash after turn k (simulated by cancelling the consumer at a fixed event) leaves k turns stored; `Resume` makes the same remaining requests as an uninterrupted run. (`TestStoppedRunResumesWithTheSameRequests`: the consumer stops at the run start and after each turn k = 0..3, a new process resumes, the requests equal an uninterrupted run's; real crashes at every store call in `TestRecoveryResumesARunKilledAnywhere`)
- [x] Two runners on one conversation: one wins the lease, the other gets `ErrLeased`; a stale writer that loses a sequence race stops without overwriting. (`TestOneRunnerHoldsTheLease`, `TestLeaseIsRenewedDuringALongTurn`, `TestLostLeaseStopsTheWriter`, `TestStaleWriterStopsWithoutOverwriting`; `TestStoreTimeoutsAreRetriedWithoutDuplicates` for appends that landed)
- [x] The cache returns the same context as a cold load after every append; it never serves entries from another tenant or conversation. (`TestCacheNeverCrossesTenantsOrConversations`: one id in two tenants, an LRU of 2 evicting, a writer behind the cache's back; invariant 5 compares the cache with a cold load after every execution)
- [x] Invariants named and checked after every run (§2.6): stored entries are a prefix-consistent copy of the harness's turn events; the stored context round-trips to the harness transcript; the lease is released on every exit path. (`world.check` and `proc.drain` in `helpers_test.go`, invariants 1–6, listed below)
- [x] Recovery: runs killed at fixed points (before the first turn, between turns, mid-flow, with two replicas running `Recover` at once) are each resumed exactly once and finish with the same transcript as an uninterrupted run. (`TestRecoveryResumesARunKilledAnywhere`: 8 kill points, before the prompt, between turns, a lost turn, before and after completing, at the release; `TestRecoveryResumesAFlowMidway`: 5 points in a flow; `TestAgentStepResumesFromTheStoreAfterACrash`; `TestTwoReplicasRecoverEachRunOnce`: two replicas, two tenants; `TestRecoveryLeavesANewRunToItsCreator`)
- [x] Retry: a model that fails retryably twice then succeeds is retried with growing delays (checked with the fake clock) and completes; one that keeps failing stops after the maximum attempts as `failed`; a non-retryable error is never retried automatically. (`TestRetryableFailureIsRetriedWithGrowingDelays`, `TestRetryStopsAtMaxAttempts`, `TestNonRetryableErrorsAreNeverRetried`: invalid request, context too long, auth, budget; `TestRecoveryLoopRunsEveryInterval`)
- [x] Approvals: a suspended run is listed by `ListSuspended`; `Decide` with approval resumes it to completion; a rejection makes it `rejected` and it is never resumed. (`TestApprovedRunCompletes`, `TestRejectedRunIsFinal`)
- [x] A new message on a conversation with an unfinished run fails with `ErrRunUnfinished`; after `Cancel` (including a run canceled before the model answered) the next message runs and its first request is a valid, alternating conversation. (`TestNewMessageWaitsForTheUnfinishedRun`; `TestCancelBeforeTheAnswerKeepsTheConversationValid`: a stopped, a live and a crashed run; `TestNextRunAppendsAMissingMarker`; `TestRunRefusesAnInvalidMessage`)
- [x] Resuming against a changed step tree fails with a distinct, non-retryable error, writes nothing, releases the lease, and a later resume with the original tree succeeds; with the explicit operator option it proceeds. (`TestChangedStepTreeWritesNothing`: a suspended and an interrupted run, the run record and checkpoint byte-compared; `TestOperatorMayResumeWithAChangedTree`)
- [x] A completed run is returned from its checkpoint without `Resume`, even when the current step tree differs. (`TestCompletedRunIsReturnedFromItsCheckpoint`)
- [x] `flow.EventWarning` reaches the run's event stream; an observer given an unknown event kind does not fail. (`TestWarningReachesTheStream`; `TestUnknownHarnessEventsArePassedOn`)
- [x] The store-backed saver passes `agentsteptest.RunSaver`; an agent step in a flow resumes from it after a simulated crash. (`TestSaverConformance`; `TestAgentStepResumesFromTheStoreAfterACrash`: killed after each of 4 turns)
- [x] With the store-backed saver, a flow checkpoint after an N-turn agent step holds a reference, not the transcript (its size does not grow with N), and the transcript reads back from the store byte-identical. (`TestCheckpointSizeDoesNotGrowWithTurns`; `TestAgentStepResumesFromTheStoreAfterACrash` compares the stored transcript's JSON with an uninterrupted `agentstep` run's)
- [x] Seeded simulation of crashes, retries and concurrent runners against `store/memory`, plus a fuzz target over the seed. Mutation list recorded. (`TestSimulation`: 300 seeds; `FuzzSimulation`; mutation list below)

## Notes

- **From the T038 review (2026-10-12):** get the Admin with `memory.Admin(s)` (SQLite: `sqlite.Admin(s)`, T029), a package function, never a method. Call it once in `cmd/` and pass it only to the recovery loop's constructor. Also add an **archtest rule**: request-handling packages (`pkg/agentapi`, `pkg/mcp/server`, connectors) must not import store backends (`pkg/store/memory`, `pkg/store/sqlite`), so they can never reach `Admin`. Only `cmd/` and tests wire backends.

- archtest: `pkg/runner` may import `harness`, `store`, `llm`, `tool`, `tenancy`, `clock`.

### API (package `runner`)

- `New(s Store, agents AgentFactory, opts ...Option) (*Runner, error)`. `Store` is `store.ConversationStore` + `store.RunStore` + `flow.Checkpointer` (every backend is all three). `AgentFactory func(ctx, conversationID) (Agent, error)`; `Agent` is anything with the harness's `Run(ctx, harness.Input) iter.Seq2[harness.Event, error]` (`*harness.Harness`).
- Options: `WithFlows(FlowFactory)`, `WithClock`, `WithOwner` (lease owner prefix; each execution owns `owner/n`), `WithLease(ttl)` (default 30s, renewed every ttl/3), `WithRetry(RetryPolicy)` (default 5 attempts, 2s doubling to 5m, jitter in [d/2, d]), `WithIDs`, `WithRand`, `WithCacheSize` (default 1024 conversations).
- `Run(ctx, conv, msg)` (chat), `RunFlow(ctx, conv, *flow.State)`, `Resume(ctx, runID, ...ResumeOption)`, `Decide(ctx, runID, path, flow.Decision, ...ResumeOption)`, `Cancel(ctx, runID) (store.Run, error)`, `Context(ctx, conv)`; `AllowShapeChange()` is the operator's per-resume option.
- They return an `*Execution` (`Run` is the record as claimed) once the run is created and claimed; `Events()` streams `Event{Kind: EventHarness|EventFlow|EventDone, RunID, Harness, Seq, Flow, Run, Result, State}` and `Wait()` drains it. An entry is stored before its event is delivered, and `Event.Seq` is its position. A failed run ends the stream with `*RunError{Run, Err}`.
- Errors: `ErrRunUnfinished` (as `*UnfinishedError{ConversationID, RunID}`), `ErrInvalidInput` (also matches `harness.ErrInvalidInput`), `ErrAwaitingDecision`, `ErrNotPending`, `ErrNotStarted`, `ErrNoFlows`, `ErrCanceled`, `ErrIterated`, `ErrInvalidConfig`, and the store's `ErrLeased`, `ErrLeaseLost`, `ErrFinal`, `ErrConflict` and `flow.ErrShapeChanged` passed through.
- Recovery: `NewRecovery(r, admin store.Admin, ...RecoveryOption)` (`WithBatch`, `WithWorkers`, `WithInterval`); `Recover(ctx) (Report, error)` makes one pass; `Run(ctx, report)` runs one at once and then one every interval.
- Agent steps: `NewSaver(cs)` is the store-backed `agentstep.TranscriptSaver` (one conversation per step, `StepConversation(runID, path)`), with `Transcript(ctx, StepOutput)`; `NewAgentStep(name, h, saver, keys, ...)` writes a `StepOutput{Answer, ConversationID, Seq}` instead of the transcript.
- Entries: `Record` (`Prompt`, `Turn`, `Canceled`) is the JSON in `Entry.Record`; `DecodeRecord` and `HarnessTurn` read it back; `CanceledText` is the marker reply.

### Decisions

- **A flow run is a run with a checkpoint.** `RunFlow` saves the first checkpoint right after create and claim (and cancels the run if that fails), so `Resume` tells flow runs from chat runs by `Checkpointer.Load`.
- **A changed step tree writes nothing.** `Resume` and `Decide` compare `flow.Fingerprint` with the checkpoint's before claiming, and return a `*flow.ShapeChangedError`; the run is not moved to `failed` (that would be a write, and would count an attempt). The run stays as it was, so the original code or `AllowShapeChange` continues it. Recovery reports it as an error on every pass for an interrupted run until someone acts.
- **Dead on arrival.** A chat run whose log has no entry of its own (the process died before its prompt was stored) is `canceled` with `ErrNotStarted` when resumed: there is no message to run. Its client must send the message again.
- **Answered but not completed.** A resumed chat run whose log ends on its own answer completes without a model call.
- **Interrupted executions** (the consumer stopped reading, `ctx` ended, `Cancel`, or a lost lease) leave the run `running` and release the lease, so recovery picks it up one lease time later. Only an execution that lost its lease does not release.
- **Stale writers.** An append that conflicts aborts the execution with `store.ErrConflict` and makes no transition: the other writer owns the run. Appends and transitions that time out are retried; an append that had landed is an identical retry (a no-op), and a transition is read back with `Get`.
- **Cancel** first transitions, then appends the marker if the log ends on a user message. If it dies in between, the next `Run` appends the marker (labelled with the canceled run) before its prompt. The marker is a `KindTurn` entry with a `Record{Canceled}`.
- **Recovery skips a brand-new run.** It leaves alone a `running` run with no lease, no attempts and less than one lease time old: it is between its creator's `Create` and `Claim`.
- **Decide on a completed run is `ErrFinal`;** `Resume` of one returns its final state from the checkpoint without running anything.
- **`AgentStep` lives in `pkg/runner`, not `pkg/agentstep`.** The T024 handoff said to change `agentstep.Output`. This task may not touch `pkg/agentstep`, so a wrapper runs the agent step and then replaces its output with a `StepOutput` before the flow checkpoints. `agentstep`'s memory and file savers are unchanged. T037 should decide whether to fold it into `agentstep`.
- **archtest:** `pkg/runner` may also import `pkg/flow` and `pkg/agentstep` (beyond harness, store, llm, tool, tenancy, clock), and in tests the memory store and the fakes. `TestRunnerDoesNotImportABackend` checks that its own code imports no `pkg/store/...` package. `pkg/agentapi` (comment) and `pkg/mcp/server` (new rule) may import no store backend; connectors already could not.

### Invariants (checked after every test by `world.check`, per execution by `proc.drain`)

1. Entries mirror events: every prompt or turn event a consumer saw is stored at the `Seq` it names, with the same messages, record and run.
2. The stored context round-trips: every context is a valid transcript, and a completed run's `Result.Transcript` is exactly the log up to its last entry.
3. No turn is stored twice: no tool call ID appears twice in a log.
4. Leases are released on every exit path: after an execution of a live process, it holds no lease, and at the end no live process does.
5. The cache equals a cold load after every execution.
6. Every model request is a valid, alternating conversation.

### Simulation

`simulate(t, seed)` in `sim_test.go` (`TestSimulation`, 300 seeds; `FuzzSimulation`) plays random chat messages on 1–3 conversations. Each execution runs normally, is abandoned at a random event, dies at a random store call (which lands or not), or is stopped and canceled. The model fails retryably or fatally now and then. Between messages, one or two recovery replicas (each liable to die) and the clock bring every run to an end, canceling fatal failures. Besides invariants 1–6, it checks each conversation's final log against a restatement of the scripted model: each run's entries are contiguous, start with its prompt and continue with a prefix of that prompt's script; a completed run's prefix is whole; a canceled run's ends with the marker exactly when it is not whole; and no run is left unfinished. `TestSimulation` asserts that every case kind occurs; over 300 seeds the mix was:

| case | count |
|---|---|
| completed at once | 216 |
| canceled by a client | 305 |
| marker | 429 |
| run not started | 115 |
| died before running | 59 |
| died, call landed | 146 |
| died, call lost | 135 |
| retried | 172 |
| retryable failure | 185 |
| fatal failure | 204 |
| fatal failure canceled | 139 |
| two replicas | 315 |
| recovery replica died | 249 |

### Mutation list

Each bug was planted on its own and the package tests run with `go test -count=1 ./pkg/runner/`. Every plant compiles, and every one fails at least one test; none survives.

Two plants survived the first pass and led to stronger checks:
- `cache-unguarded`: a reader now refreshes the cache between a landed append and its retry.
- `saver-wrong-kind`: the agent-step test now checks the kind and run of every entry in the step's conversation.

`lost-lease-ignored` used to show up only as a hang. TestLostLeaseStopsTheWriter now has an escape that only a broken writer reaches, so the hang becomes a failure. No catch depends on timing.

| Plant | Bug | Failing tests |
|---|---|---|
| `turn-twice-retry` | **Required: a turn persisted twice after a crash.** An append that timed out but landed is retried at the next position | TestStoreTimeoutsAreRetriedWithoutDuplicates |
| `turn-twice-resume` | **Required: a turn persisted twice after a crash.** Resuming a run whose answer is stored asks the model again | TestRecoveryResumesARunKilledAnywhere, TestTwoReplicasRecoverEachRunOnce |
| `lease-not-renewed` | **Required: the lease not renewed** | TestLeaseIsRenewedDuringALongTurn |
| `lost-lease-ignored` | A lost lease does not stop the writer | TestLostLeaseStopsTheWriter (through its escape: the writer that never stops is released after 30 s and writes) |
| `release-skipped-on-stop` | An interrupted execution keeps its lease | TestCancelBeforeTheAnswerKeepsTheConversationValid, TestNextRunAppendsAMissingMarker, TestStoppedRunResumesWithTheSameRequests, TestSimulation, FuzzSimulation |
| `final-run-rerun` | **Required: recovery resuming a final run.** A completed run is resumed instead of returned from its checkpoint | TestCompletedRunIsReturnedFromItsCheckpoint, TestAgentStepResumesFromTheStoreAfterACrash |
| `final-check-dropped` | **Required: recovery resuming a final run.** Resume does not check for a final run itself; the store's Claim still refuses it | TestApprovedRunCompletes |
| `not-started-run` | A run whose prompt was never stored is run with no input instead of canceled | TestRecoveryResumesARunKilledAnywhere, TestRecoveryResumesAFlowMidway, TestSimulation, FuzzSimulation |
| `recovery-no-grace` | Recovery resumes a run between its creation and its claim | TestRecoveryLeavesANewRunToItsCreator |
| `retry-ignores-attempts` | **Required: retry ignoring `Attempts`** | TestRetryStopsAtMaxAttempts, TestRetryableFailureIsRetriedWithGrowingDelays |
| `retry-everything` | Every error is retried | TestNonRetryableErrorsAreNeverRetried, TestSimulation |
| `delay-not-growing` | The retry delay does not grow | TestRetryableFailureIsRetriedWithGrowingDelays |
| `cancel-edits` | **Required: cancel editing the transcript instead of appending.** The marker overwrites the user message | TestCancelBeforeTheAnswerKeepsTheConversationValid, TestNewMessageWaitsForTheUnfinishedRun, TestSimulation, FuzzSimulation |
| `next-run-drops-user` | **Required: cancel editing the transcript instead of appending.** The next run drops a dangling user message from its request instead of appending the marker | TestNextRunAppendsAMissingMarker |
| `no-marker` | Cancel appends no marker | TestCancelBeforeTheAnswerKeepsTheConversationValid, TestNextRunAppendsAMissingMarker, TestSimulation, FuzzSimulation |
| `cache-unguarded` | The cache applies an entry that does not extend it | TestStoreTimeoutsAreRetriedWithoutDuplicates (the reader case) |
| `cache-no-refresh` | A cache hit is not brought up to date | TestStaleWriterStopsWithoutOverwriting |
| `cache-no-tenant` | The cache key ignores the tenant | TestCacheNeverCrossesTenantsOrConversations |
| `conflict-fails-run` | A stale writer marks the run failed | TestStaleWriterStopsWithoutOverwriting |
| `shape-unchecked` | A changed step tree is not checked before the claim | TestChangedStepTreeWritesNothing |
| `allow-shape-ignored` | `AllowShapeChange` is ignored | TestOperatorMayResumeWithAChangedTree |
| `reject-not-final` | A rejection leaves the run failed instead of rejected | TestRejectedRunIsFinal |
| `suspend-lost` | A suspended flow is stored as failed | TestApprovedRunCompletes, TestRejectedRunIsFinal, TestChangedStepTreeWritesNothing, TestOperatorMayResumeWithAChangedTree, TestCompletedRunIsReturnedFromItsCheckpoint, TestWarningReachesTheStream |
| `warning-dropped` | Flow warnings are not passed on | TestWarningReachesTheStream |
| `unknown-event-fails` | An unknown harness event fails the run | TestUnknownHarnessEventsArePassedOn |
| `prompt-not-stored` | The prompt entry is not stored | TestRunStoresThePromptAndEveryTurn, Example and 20 others |
| `saver-wrong-kind` | The saver stores every entry as a turn | TestAgentStepResumesFromTheStoreAfterACrash |
| `stepoutput-seq` | The step output references the entry before the last | TestAgentStepResumesFromTheStoreAfterACrash, TestCheckpointSizeDoesNotGrowWithTurns |
| `step-output-transcript` | The checkpoint keeps the whole agent output | TestCheckpointSizeDoesNotGrowWithTurns |
| `usage-dropped` | A completed run's usage is not stored | TestRunStoresThePromptAndEveryTurn |

### For T037 (API wiring)

- Wire in `cmd/agentkit`: `s := sqlite.Open(...)`, then `runner.New(s, agents, runner.WithFlows(...), runner.WithOwner(hostname+pid))`. Call `sqlite.Admin(s)` once and pass it only to `runner.NewRecovery`, and run `rc.Run(ctx, report)` inside `serve`. `pkg/agentapi` gets `pkg/runner` and `pkg/store` in archtest, never a backend.
- Map the errors:
  - `*runner.UnfinishedError` → 409 with `RunID`;
  - `ErrInvalidInput` → 400;
  - `store.ErrLeased` → 409 (running elsewhere);
  - `store.ErrFinal` → 409;
  - `ErrAwaitingDecision` and `ErrNotPending` → 409 or 422;
  - `flow.ErrShapeChanged` → 409 with a clear code (the operator decides; `AllowShapeChange` behind an admin flag);
  - `store.ErrNotFound` → 404;
  - `*runner.RunError` → the run's failure in the body, with `Run.Retryable` and `NextAttemptAt`.
- A client disconnect stops reading `Events()`. The run stays `running` with its lease released, and recovery resumes it after one lease time. If a foreground chat should instead end `canceled` on disconnect, call `Cancel` from the handler.
- `ErrNotStarted`: a run whose process died before its prompt was stored ends `canceled`. Tell the client to resend.
- Usage of an execution that was interrupted is in its turn records (`Record.Turn.Usage`) but not added to `Run.Usage`, because no transition happened. Sum the turn records if billing needs it.
- Flow runs: `RunFlow` takes the initial state; `EventFlow` events carry `flow.EventWarning` (and kinds added later: pass them on).
- `Context(ctx, conv)` is the cached transcript for `GET /v1/conversations/{id}`; history paging stays on `ConversationStore.History`.
- T092's empty conversations need nothing from the runner. `Run` on a conversation with no entries starts it, so the conversation record T092 asks the store for stays outside `pkg/runner`.

### For T071 and T073 (background runs, messaging)

- A background run is an `Execution` nobody reads after start. Call `Wait()` in a goroutine the runner's owner keeps (the execution ends at the first stop, so a reader that goes away leaves the run to recovery). The event log (T090) should be written from the same loop, before or with each event; `Event.Seq` gives turn events a durable position.
- Cancel is already idempotent at the store level: canceling a final run is `ErrFinal`, so the API should treat `ErrFinal` on cancel as success. Within one process, `Cancel` stops a live execution and waits for it, then the marker rule applies. **Across replicas it does not:** a run executing in another process gives `store.ErrLeased`. T071 needs a cancel signal on the bus (E18) that the owning process turns into `Cancel`, or must retry after the lease expires.
- Restart: recovery resumes `running` runs one lease time after the dead process's last renewal, and `failed` + retryable ones when due. A resumed chat run makes exactly the remaining requests. A tool call in flight when the process died is asked of the model again (see agentstep's side-effects note), so messaging tools must dedupe by message ID (T073's exactly-once processing), not by tool-use ID.
- A wall-clock limit (T071) can be `ctx` with a deadline on the execution. The run is then left `running`, so T071 must transition it to `failed` itself, or the runner gains an option for it. Budgets are the harness's (`ErrBudgetExceeded`, not retried).
- T073's wake-on-reply is `Run(ctx, conv, replyMessage)` on the sender's conversation. It fails with `ErrRunUnfinished` while the sender's run is still going, so the mailbox must queue and retry after that run ends (or deliver into a waiting tool call).
- Interrupted executions' usage is not in `Run.Usage` (see T037). Per-thread budgets should sum turn records.
