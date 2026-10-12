---
id: T023
title: "pkg/flow: steps, combinators, checkpoints, resume, approval"
epic: E04
status: done
depends_on: [T001, T002]
---

# T023: `pkg/flow`

## Context

Multi-step and multi-agent execution as plain Go plus a few combinators ([PLAN.md `flow`](../docs/PLAN.md#flow)). The value over hand-written Go is that every step boundary is a checkpoint, so runs resume after a crash and can pause for a human.

## Design

- `Step` interface: `Run(ctx, *State) error`; `flow.Func` adapts a plain function. A harness run becomes a `Step` in T024.
- `State`: named, JSON-serializable values (`Get[T]`, `Set`) so it can be checkpointed.
- Combinators: `Sequence`, `Parallel` (each branch gets a copy of the state; results merge by key, and a conflict is an error), `Loop` (until a condition or a maximum), `Route` (pick the next step by a key computed from state), `Approve(name)` (suspends the run until `Resume` is called with a decision).
- Runner: `flow.Run(ctx, id, step, opts...)` saves a checkpoint after every completed step through a `Checkpointer` interface; `flow.Resume(ctx, id, step)` loads it and skips completed steps.
- `Checkpointer` with an in-memory implementation now; `flowtest.RunCheckpointer` conformance suite so the SQLite store (E05) can implement it later.
- One event type for all steps (started, finished, failed, suspended), with the step path (e.g. `review/parallel[1]/search`).

## Acceptance criteria

- [x] `Sequence` runs steps in order and stops at the first error. — `TestSequenceRunsInOrderAndStopsAtFirstError`
- [x] `Parallel` runs branches concurrently, merges their state, and reports a key conflict as an error; one branch failing cancels the others. — `TestParallelMergesDisjointKeys`, `TestParallelMergeIsIndependentOfFinishOrder`, `TestParallelKeyConflictIsError`, `TestParallelFailureCancelsSiblings`
- [x] `Loop` stops when its condition holds and errors at its maximum. — `TestLoopStopsWhenConditionHolds`, `TestLoopErrorsAtMaximum`
- [x] `Route` runs the chosen step; an unknown route key is an error. — `TestRouteRunsChosenStep`, `TestRouteUnknownKeyIsError`
- [x] After a failure in step 3 of 4, `Resume` runs only steps 3 and 4, with the state as it was after step 2. — `TestResumeRunsOnlyRemainingSteps` (also `TestResumeAfterCancellation`)
- [x] `Approve` suspends the run (`ErrSuspended`), and `Resume` with approval continues while rejection ends the run with `ErrRejected`. — `TestApproveSuspendsThenResumeApproves`, `TestApproveRejectionEndsRun` (also `TestParallelBranchSuspendsWithoutCancellingSiblings`)
- [x] Nested combinators checkpoint and resume correctly (a sequence inside a parallel inside a loop). — `TestNestedCombinatorsCheckpointAndResume`
- [x] The in-memory `Checkpointer` passes `flowtest.RunCheckpointer`. — `TestMemoryCheckpointer`
- [x] Events report each step's path and outcome in order. — `TestEventsReportPathsAndOutcomes`

## Notes

- Durable-execution engines (DBOS, Temporal, Restate) can sit behind `Checkpointer` later if runs must survive for days.
- Dependencies: standard library only; no archtest change.

### Mutation check

Each bug below was planted alone (the code compiled), `go test -race ./pkg/flow/...` was run, and the code was restored afterwards.

| Planted bug | Failing tests |
|---|---|
| Checkpoint written before the step's state changes are done | `TestCheckpointIsSavedAfterTheStepHasFinishedChangingState`, `TestResumeRunsOnlyRemainingSteps`, `TestApproveSuspendsThenResumeApproves`, `TestNestedCombinatorsCheckpointAndResume` and 7 more |
| Resume re-runs a finished step | `TestResumeRunsOnlyRemainingSteps`, `TestNestedCombinatorsCheckpointAndResume`, `TestApproveSuspendsThenResumeApproves` and 5 more |
| `Parallel` does not cancel siblings on failure | `TestParallelFailureCancelsSiblings` |
| `Parallel` silently overwrites a conflicting key | `TestParallelKeyConflictIsError` |
| `Loop` ignores its maximum (also: one iteration too many) | `TestLoopErrorsAtMaximum` |
| `Route` with an unknown key runs nothing and returns nil | `TestRouteUnknownKeyIsError` |
| `Approve` rejection treated as approval | `TestApproveRejectionEndsRun` |
| Non-deterministic step paths (branch index taken from goroutine start order) | `TestPathsAreDeterministicUnderScheduling`, `TestEventsReportPathsAndOutcomes`, `TestNestedCombinatorsCheckpointAndResume` and 2 more (fails on 10 of 10 single runs) |
| `Memory` stores and returns checkpoints without copying (suite check) | `TestMemoryCheckpointer/StoresCopies` |

### Invariants and simulation (added later, write-tests skill steps 4 to 8)

- Every `Run`/`Resume` in the package's tests goes through `run`/`resume`/`runCtx`/`resumeCtx` (`helpers_test.go`), which check the invariants I1 to I13 listed in `invariants_test.go` after each call and, across the calls of one run id, when the test ends. A test's own observer is passed as `observer(fn)`, not `flow.WithObserver`.
- `sim_test.go`: `TestSimulatedFlows` (300 seeds, about 1 s under `-race`) and `FuzzSimulatedFlow` build random trees with faults (a step failing or panicking once, cancellation inside a step, at an event or after a save, a failed save, partial or no decisions) and check S1 to S8: the uninterrupted run matches an independent reference interpreter, the crashed-and-resumed history ends in the same state with every step finished exactly once, and more. `TestSimulationCoversEveryCase` keeps the generator honest.
- **Bug found by the simulation, since fixed** (`a645012`, test `TestResumeOfARejectedRunIsRejectedEvenIfASiblingFails` in `rejected_resume_test.go`). Resuming a rejected run ran again the `Parallel` branches the rejection had cancelled; if one of them failed, `Resume` returned that error instead of `ErrRejected`. When the tests were merged, I13's temporary tolerance for it was removed and I13 now also asserts that such a Resume emits no events. Re-planting the bug fails that test, `TestSimulatedFlows` and `TestApproveRejectionEndsRun`. Also on merge: the per-call helper keeps `main`'s fingerprint check on every saved checkpoint (`withShapeCheck`), and I3 follows T032's later rule that a fingerprint of another encoding version only warns (`ErrNoShape`).

Mutation check of the invariants and the simulation (`-race -shuffle=on -count=2`; "sim" = `TestSimulatedFlows`/`FuzzSimulatedFlow`):

| Planted bug | Original tests | Round 1 | Round 2 |
|---|---|---|---|
| `complete` edits the previous checkpoint in place | 1 test | 25 tests + sim | same |
| a completed step keeps its children's records | 1 test | 20 tests + sim | same |
| children dropped by name prefix without separator (`a` drops `ab`) | survived | sim | sim |
| Finished emitted before the save | 1 test | 27 tests + sim | same |
| no ctx check before a step starts | survived | sim | sim |
| branch index from goroutine start order | 6 tests (by luck of scheduling) | 2 tests (sim stalled) | 7 tests + sim (152 seeds) |
| `wg.Add` inside the goroutine | 12 tests | 15 tests + sim | same |
| `Loop` runs one iteration too many | 1 test | 1 test + sim | same |
| `Route` path leaves out the key | survived | sim | sim |
| `Resume` starts from an empty state | survived | sim | sim |
| a skipped step's record not restored | 9 tests | 10 tests + sim | same |
| a rejection not recorded | 1 test | 1 test + sim | same |
| pending approvals not sorted | survived | **survived** | sim (seeds 145, 247, every run) |
| merge ignores deletions | 2 tests | 2 tests + sim | same |
| equal writes by two branches not a conflict | 1 test | 1 test | 1 test + sim |
| rejected run saved as failed | 1 test | 1 test + sim | same |
| `Memory` stores without copying | suite | suite | suite |
| `Resume` keeps the old fingerprint | 3 tests | 3 tests | 3 tests |
| observer calls not serialized | survived | **survived** | 13 tests + sim (race detector) |
| cancelled sibling's error reported instead of the cause | 1 test | 1 test + sim | same |
| a suspended branch cancels its siblings | 1 test | sim | 2 tests + sim |

Between the rounds: the observer check (I3: never called concurrently, through an unlocked field the race detector watches); every leaf checks it runs at the path the tree gives *it* (S4); test-side gates keyed by node, with a guard that only gives up the order, so a path bug fails instead of hanging; and inputs the generator never made: Parallels wider than 10 (where `[10]` sorts before `[2]`), a first `Run` with no decision at all, two branches writing the same value.

### Extra tests beyond the criteria

Each guards a design trap: `TestCheckpointIsSavedAfterTheStepHasFinishedChangingState` (a failed step's partial writes never reach a checkpoint; a finished step is durable by the time `Finished` is emitted), `TestPathsAreDeterministicUnderScheduling` and `TestParallelMergeIsIndependentOfFinishOrder` (gates force finish order, no sleeps), `TestCompletedStepsDropTheirChildRecords`, `TestValidateRejectsMalformedFlows`, `TestRunAndResumeRefuseBadRequests`, `TestCheckpointSaveFailureFailsTheStep`, `TestPanicBecomesError`, `TestCheckpointJSONGolden` (the persisted format, `testdata/checkpoint_suspended.json`), `TestState*` and `FuzzStateUnmarshal` (decoding never panics; whatever decodes re-encodes to a fixed point), `ExampleRun` (sequence with an approval).

### Design decisions

- **A step's identity is its path**, built from names and indices only: `seq/step`, `par[i]/step` (i = branch index in the code, not finish order), `loop[i]/body`, `route[key]/step`. Every step needs a name: `Func(name, fn)`, combinators take one, any other step has a `Name()` method or is wrapped in `flow.Named`. `Validate` (run by `Run`/`Resume`) rejects missing, duplicate or malformed names with `ErrInvalidFlow`. Validation is at run time because combinator constructors return a `Step`, not `(Step, error)`.
- **Checkpoint = state after each completed step, keyed by path.** A step with a record is skipped on resume and its state restored. A step inside a `Parallel` branch records the branch's own state. A completed step drops the records of its children, so a checkpoint holds only the frontier of the run (except loop iterations, which accumulate until the loop completes). The record is taken after `Run` returns, never before.
- **`Loop` conditions and `Route` keys are re-evaluated on resume**, on the restored state, so they must be pure functions of the state. The step tree given to `Resume` must be the one that produced the checkpoint; since T032 this is detected (`flow.Fingerprint`, `ErrShapeChanged`, opt-out `flow.WithAllowShapeChange`).
- **`Parallel` conflict = two branches changed the same key**, even to the same value; a delete counts as a change. The merge is all-or-nothing. Of several failures the lowest-numbered branch that failed on its own is reported. A branch waiting at `Approve` does not cancel its siblings; `Parallel` returns a `SuspendedError` with every pending approval after they finish.
- **`Parallel` is a barrier (fork and join), not background work.** It does not start work that outlives the step.
- **`Run` returns `(*State, error)`; the state is nil on error.** `WithCheckpointer` is required (`ErrNoCheckpointer`). `Run` fails with `ErrRunExists` if the id has a checkpoint. One writer per run id at a time.
- **A rejection is final**: it is recorded in the checkpoint, and resuming again returns `ErrRejected` instead of asking again.
- **Events**: `started`, `finished`, `failed`, `suspended`; one `Event{Kind, Path, Err}` type. `Finished` is emitted after the checkpoint is saved. A combinator's events surround its children's, so a failure appears on every enclosing path. Steps skipped on resume emit nothing. The observer is called serially.
- A panic in a step is recovered into `*PanicError`. A cancelled context stops the run with the context's error; a step that finished but whose save was refused because of the cancellation runs again on resume.

### For T024 and later

- Wrap the harness run in a `Step` with a `Name()` method (or `flow.Named`). Its state keys are its own business, but two parallel agents must not write the same key.
- Pass `flow.WithCheckpointer(flow.NewMemory())` for now; the SQLite store (E05) implements `flow.Checkpointer` and runs `flowtest.RunCheckpointer`. `Checkpoint` is plain JSON, so it can be one row or document.
- To show an approval prompt, read `SuspendedError.Paths` (or `Checkpoint.Pending`), and resume with `flow.WithDecision(path, flow.Decision{Approved: ..., Reason: ...})`.
- `flowtest.Recorder` collects events for tests.
- A step must be safe to run again from the state it started with: a crash mid-step re-runs it, so side effects need to be idempotent. A harness run should resume from its transcript rather than start over.
- Not built: a way to resume a single branch of a `Parallel` independently, a checkpoint-size limit, and compare-and-swap on `Save` (the contract is one writer per run id).

### Fixed after T023

- 2026-10-12 (planner, owner-approved): **a rejected run stays rejected on `Resume`.** Found by the T013 dry run. When a rejection inside a `Parallel` cancelled an unfinished sibling, `Resume` ran that sibling again, could return its error instead of `ErrRejected`, and saved the run as `failed`. `Resume` now returns the recorded `RejectedError` (smallest rejected path) for a rejected checkpoint, with no step run, no event and no save. Regression test: `TestResumeOfARejectedRunIsRejectedEvenIfASiblingFails` (its event and save checks fail on every run without the fix; the sibling-error symptom itself depends on scheduling).
