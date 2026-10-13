---
id: T098
title: "runner: keep a run's turns and store them in batches"
epic: E05
status: done
depends_on: [T030, T088]
---

# T098: turn buffer

## Context

A chat run wrote one entry per turn, so a 3-tool run made 10 writes: create, claim, renew, the prompt, 4 turns, the transition and the release. On SQLite, a single-writer store, that caps throughput (T030 Notes, finding 1). The owner's direction (2026-10-13): runners may read and claim from the database, but a run must not write the database on every turn unless its flush size is 1. The size is set per run invocation, defaults to 10, and runs stay isolated: there is no batching across runs. Live runs are tracked so that an emergency shutdown can flush them.

## Scope

- **Store:** `ConversationStore.AppendBatch(ctx, id, entries)` writes consecutive entries all or none, by `Append`'s rules for each. It is implemented on the memory, SQLite and Postgres stores and covered by the `storetest` conformance suite.
- **Harness:** `ToolRecord.Kind` records each call's `tool.Metadata.Class` kind, so the runner knows which turns may have changed the world.
- **Runner:**
  - **Flush size:** `WithFlushEvery(n)` sets the runner's default (10). `FlushEvery(n)` sets it per run on `Run`; it is kept in the prompt entry's record, so a resume (or the recovery loop) honours it.
  - **When turns are stored:** the prompt is stored at once. Turns are stored in one write:
    - every N turns;
    - after a turn that called a tool not declared `KindRead` (the user's "flush first", before the next model call);
    - when the agent stops, for any outcome, before the run's transition;
    - when the execution is interrupted (consumer stop, ctx, `Cancel`, `Shutdown`), but not after a lost lease.
  - **One-entry flushes:** a flush of a single entry uses `Append`.
  - **`EventSaved`:** tells the consumer how far the log is stored. `EventHarness.Seq` is now the position an entry takes, not a stored position.
  - **`Runner.Shutdown(ctx)`:** stops every live execution (`ErrShutdown`), which stores its turns and releases its lease, and refuses new runs and resumes.
  - **Renew:** a freshly claimed execution no longer renews its lease when iteration starts.

## Acceptance criteria

- [x] **Suite:** `AppendBatch` passes the conformance suite on all three stores (Postgres in the integration tier). (`storetest` `AppendBatch`: Rules, ReadsBackAsAppendedOneByOne, RetriesAreNoOps, EmptyBatchIsANoOp, RacingBatchesExactlyOneWins)
- [x] **Write count:** a read-only 3-tool run makes 6 writes: create, claim, prompt, one batch, transition, release. With `FlushEvery(1)` it makes one append per turn. (`TestReadOnlyTurnsAreStoredTogether`)
- [x] **Every N:** batches of N, and the rest at the end. (`TestTurnsAreStoredEveryN`)
- [x] **Flush first:** a turn that called a tool not declared read-only is stored before the next model call; read-only turns wait. (`TestTurnsThatMayChangeTheWorldAreStoredBeforeTheNextModelCall`)
- [x] **Crash:** a crash loses only unstored turns; the resume redoes exactly those (model calls counted), ends as an uninterrupted run would, and keeps the run's `FlushEvery`. (`TestCrashRedoesOnlyUnstoredTurns`)
- [x] **Interruption:** a consumer that stops keeps the turns done so far, and the resume does only the rest. (`TestInterruptedRunStoresItsTurns`)
- [x] **Lost lease:** a writer that lost its lease writes nothing more, kept turns included. (`TestLostLeaseStopsTheWriter/read`)
- [x] **Shutdown:** turns stored, leases released, new runs refused, another process continues. (`TestShutdownStoresTurnsAndLeavesRunsToOthers`)
- [x] **Late iteration:** an execution iterated late in its claim renews before running; a fresh one does not. (`TestLateExecutionRenewsItsClaim`)
- [x] **Tool kinds:** a declared read records `KindRead`; an undeclared tool and an unlisted name record `KindWrite`. (`harness.TestToolFailuresBecomeErrorResults`)
- [x] **Crash anywhere, buffered:** the simulation draws read-only tools for half its worlds and a `FlushEvery` of 1-3 for half its runs, and requires a crash during `AppendBatch` in its case mix. Recovery resumes a run killed while writing its kept turns, whether or not the batch landed, with exactly the uninterrupted run's remaining requests. A store timeout on `AppendBatch`, landed or not, is retried without duplicates. (`TestSimulation`, `FuzzSimulation` (60 s, 160k execs), `TestRecoveryResumesARunKilledAnywhere/{losing,after}_the_kept_turns`, `TestStoreTimeoutsAreRetriedWithoutDuplicates/AppendBatch*`)
- [x] **Mutation list** recorded (Notes).

## Notes

### Decisions

- **Only declared reads are buffered.** `Metadata.Class` fails closed: a tool that declares no kind is a write, so a run over unannotated tools still stores every turn as it completes, exactly as before (minus the renew). The saving needs tools that declare `KindRead`.
- **Flush after the turn, not before the tool.** The harness invokes a tool as soon as it emits `EventToolStart`, so the runner cannot store anything before a tool runs. Storing the turn that ran a write tool before the next model call gives the same guarantee as one write per turn: a crash loses at most that turn in flight.
- **No timed flush.** Lost work is bounded by count (N-1 read-only turns), not time. A timer would need a lock around the buffer and serialized flushes, and nobody asked for it.
- **The prompt is never buffered.** A run whose prompt is not stored has not started, and its resume cancels it (`ErrNotStarted`). Buffering the prompt would lose the user's message on a crash.
- **No flush after a lost lease.** The run's next execution may be writing the same positions. A conflict would be caught (`ErrConflict`), but not writing is simpler and safer.
- **Event positions may be reused.** After a crash, positions past the last `EventSaved` are taken by the resumed run's entries. Nothing outside `pkg/runner` imports it yet (T037 wires it into the API), so no event ID scheme carries an attempt number yet. A stream built on runner events should key on the stored log, or on `EventSaved`.

### Invariants (helpers_test `world.check`)

Invariant 1 changed:
- **Before:** every prompt or turn event is stored as seen.
- **Now:** every event at or before the execution's last `EventSaved` is stored as seen.
- `EventSaved` positions increase and cover every earlier event.
- An execution that completes has every named entry saved.

The others are unchanged.

### Mutation list

Each plant was run against `go test ./pkg/runner/` and then restored. All caught, except where noted.

| Plant | Caught by |
|---|---|
| Flush after a lost lease | `TestLostLeaseStopsTheWriter/read` |
| Read-only turns treated as writes | `TestReadOnlyTurnsAreStoredTogether`, `TestTurnsAreStoredEveryN`, `TestCrashRedoesOnlyUnstoredTurns`, `TestTurnsThatMayChange…` |
| No turn treated as a write | `TestTurnsThatMayChange…/write`, `TestRecoveryResumesARunKilledAnywhere`, `TestStaleWriterStopsWithoutOverwriting`, … |
| No final flush | invariant 1, `TestCrashRedoesOnlyUnstoredTurns`, `Example`, … |
| Threshold off by one (`>` for `>=`) | `TestTurnsAreStoredEveryN`, `TestReadOnlyTurnsAreStoredTogether` |
| Resume ignores the stored `FlushEvery` | `TestCrashRedoesOnlyUnstoredTurns/every_turn` |
| The view never reads `FlushEvery` back | `TestCrashRedoesOnlyUnstoredTurns/every_turn` |
| No renew at the start | `TestLateExecutionRenewsItsClaim` |
| Renew at the start only once expired | `TestLateExecutionRenewsItsClaim` |
| `EventSaved` sent before the write | invariant 1 (crash tests, simulation) |
| Turn `Seq` off by one | invariant 1, `Example`, … |
| Pending turns not cleared after a flush | invariant 1, simulation, … |
| `Shutdown` does not stop executions | `TestShutdownStoresTurns…` (by hanging until the test timeout) |
| `Shutdown` does not refuse new runs | `TestShutdownStoresTurns…` |
| **Survives:** the cache not told of a batch | Not observable: the next load catches up by reading `History` past the cached position, so it costs reads only. The same holds for single appends since T030. |
| **Survives:** a turn flushed in the loop after an interruption | The guard matters only if the lease is lost between a turn's event and its flush. That window cannot be reached without a hook inside `emit`, and a write there would hit the next execution's `ErrConflict`. |

### Performance

Same machine and caveats as T030 (`go test -run '^$' -bench 'BenchmarkRun$' -benchtime=1000x ./pkg/runner/`). `tools=3/sqlite+read` declares the echo tool read-only.

| Run | Commits per run |
|---|---|
| 0 tools, before → now | 7 → 6 (no renew) |
| 3 write tools, before → now | 10 → 9 (no renew) |
| 3 read tools, before → now | 10 → 6 (prompt + one batch) |

The times on SQLite were too noisy on the shared machine to compare: 1.0 to 6 ms per run in both variants, at `-cpu=1` and in parallel. Allocations per 3-tool run on SQLite fell from 1,285 to 1,133. Rerun the benchmark on a quiet machine before quoting time savings.

### For T099 (queue ordering)

`Run` now takes `...RunOption`, so a later `Schedule` or `Enqueue` can share the same options.
