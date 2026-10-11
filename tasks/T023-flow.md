---
id: T023
title: "pkg/flow: steps, combinators, checkpoints, resume, approval"
epic: E04
status: todo
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

- [ ] `Sequence` runs steps in order and stops at the first error.
- [ ] `Parallel` runs branches concurrently, merges their state, and reports a key conflict as an error; one branch failing cancels the others.
- [ ] `Loop` stops when its condition holds and errors at its maximum.
- [ ] `Route` runs the chosen step; an unknown route key is an error.
- [ ] After a failure in step 3 of 4, `Resume` runs only steps 3 and 4, with the state as it was after step 2.
- [ ] `Approve` suspends the run (`ErrSuspended`), and `Resume` with approval continues while rejection ends the run with `ErrRejected`.
- [ ] Nested combinators checkpoint and resume correctly (a sequence inside a parallel inside a loop).
- [ ] The in-memory `Checkpointer` passes `flowtest.RunCheckpointer`.
- [ ] Events report each step's path and outcome in order.

## Notes

- Durable-execution engines (DBOS, Temporal, Restate) can sit behind `Checkpointer` later if runs must survive for days.
