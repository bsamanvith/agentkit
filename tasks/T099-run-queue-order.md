---
id: T099
title: "runs: start time, lane and priority, and claiming the next run in that order"
epic: E05
status: review
depends_on: [T030, T088, T098, T100]
---

# T099: run queue order

## Context

Today a run is created and claimed by the process that starts it, and the recovery loop picks up the rest oldest first (`ListResumableAll`). The owner's direction (2026-10-13): keep Postgres as the queue (no Redis for start, resume or cancel requests for now). Runners read and claim from it, in this order:
1. start time (a run may be scheduled for later, e.g. by a future cron);
2. then lane;
3. then priority.

Within a lane, the order is **priority first, then time**. A lane is a named partition for now. Integer partition keys for sharding come later.

## Scope

- **Runs table:** add three columns, by migration 0006 on SQLite and Postgres (0005 is T100's `interactive`):
  - `lane TEXT NOT NULL DEFAULT ''`;
  - `priority INT NOT NULL DEFAULT 0`;
  - `start_at BIGINT`, backfilled from `created_at`.

  The memory store gets the same fields.
- **Due time:** a run is due at a time that depends on its state:
  - running and unleased: `start_at`;
  - running with a lease: the lease's expiry, if later;
  - failed and retryable: `next_attempt_at`.

  `Resumable`, `ListResumable`, `ListResumableAll` and their SQL predicate respect `start_at <= now` on every backend, with `storetest` cases.
- **Index:** a partial index on `(lane, priority DESC, due)` over the runs that can be claimed.
- **`Admin.ClaimNext(ctx, lanes, owner, ttl)`:** claims the next due background run of the given lanes (never an interactive one, T100), highest priority first, then earliest due. On Postgres it uses `FOR UPDATE SKIP LOCKED`, so runners never wait on each other. It returns the tenant and the run.
- **`Create` options:** `store.WithLane`, `WithPriority`, `WithStartAt`, added to the variadic `store.CreateOption` that T100 introduces.
- **Runner:**
  - `Schedule` (or `Enqueue`) creates a run without claiming it and stores its prompt; it takes `Run`'s `RunOption`s (T098).
  - The recovery loop gets a lane list and claims with `ClaimNext` instead of listing then claiming. It may be renamed to a dispatcher.

## Acceptance criteria

- [x] `storetest`: claim order (priority, then due time) within a lane; lanes isolated; a run not yet due never claimed; two claimers never get the same run. Covered on all three stores, Postgres in the integration tier.
- [x] Runner: a scheduled run starts at its time, through the dispatcher; its prompt is stored once.
- [x] Migration 0006 backfills existing runs, and leaves them claimable as before.
- [x] Benchmark: `ClaimNext` under parallel claimers on Postgres; the plan uses the index (`EXPLAIN`).
- [x] Mutation list recorded.

## Notes

### Tests per criterion

| Criterion | Test | Tier |
|---|---|---|
| Claim order, lanes, not-due, two claimers | storetest Admin `ClaimNextTakesPriorityThenDueTime`, `ClaimNextKeepsLanesApart`, `ClaimNextNeverTakesARunThatIsNotDue`, `ClaimersNeverGetTheSameRun` (plus `ClaimNextLeasesTheRun`, `ClaimNextChecksItsArguments`, and a `claimNext` op in the run simulation); `start_at` in `ListResumable`: `ListResumableWaitsForTheStartTime` | unit (memory, SQLite), integration (Postgres) |
| Scheduled run starts at its time, prompt stored once | `TestEnqueuedRunStartsAtItsTimeThroughTheDispatcher` (pkg/runner) | unit |
| Migration backfills and leaves runs claimable | `TestRunsFromBeforeTheQueueAreQueued` (sqlite, postgres) | unit, integration |
| Benchmark and plan | `BenchmarkClaimNext`, `TestClaimNextUsesTheQueueIndex` (pkg/store/postgres/queue_test.go) | integration |

Extras, each for a rule the scope adds: `QueueOptionsAreStored`, `CreateWithALease` (storetest); `TestDispatcherTakesItsLanesByPriority`, `TestEnqueueRefusals`, `TestEnqueueCancelsARunWhosePromptIsNotStored`, `TestDispatcherBacksOffARunItCannotResume`, `TestDispatcherRunsARunOncePerPass` (runner); `TestAdminRole` now covers `ClaimNext` too.

Benchmark (20k mixed runs seeded, 8 parallel claimers, local Postgres): about 0.36 ms per claim at GOMAXPROCS 4 (0.9 ms at 1). `EXPLAIN` of the claim is an `Index Scan using runs_queue` per lane, no sequential scan, for one lane and for two.

### Decisions

- **Migration numbers.** SQLite `0009_queue.sql`, Postgres `0010_queue.sql`, not 0006: T037, T090 and the toolsets took the numbers in between. On Postgres the backfill lifts `FORCE ROW LEVEL SECURITY` for its `UPDATE` and sets it again in the same transaction (the migrating role owns the table but is not a superuser); the migration test checks it is set again.
- **One due time.** `Run.Due()` is the latest of `StartAt`, the lease's expiry and `NextAttemptAt`, for every claimable run, instead of the scope's per-state wording. It is the same in every case the scope names (a running run has no next attempt; a failed one has no lease after its transition), and it makes a leased run due when its lease ends. The index orders by that expression.
- **`lease_expires` stays in the index** (review, measured). With it, every lease renewal changes an indexed expression, so it is not a HOT update and writes a tuple into each of the runs table's seven indexes. Without it (index on `(lane, priority DESC, GREATEST(start_at, next_attempt_at), tenant, id)`, the full due time as a filter), renewals are HOT, but a claim steps over every held run of the lane that started before the due ones, one heap fetch each, and the order among due runs is by start rather than by due. Measured on local Postgres 16 (20k background rows; H held runs plus 2000 due ones in one lane, same priority; 20k renewals and 2000 claims, 8 at once; two runs each, machine load about 20):

  | | renew, µs/op | renewals HOT | claim, µs/op |
  |---|---|---|---|
  | with lease, H = 200 | 136 / 166 | 1% | 188 / 362 |
  | without, H = 200 | 80 / 93 | 83 to 93% | 623 / 731 |
  | with lease, H = 2000 | 160 / 111 | 5% | 327 / 172 |
  | without, H = 2000 | 91 / 52 | 61 to 87% | 1818 / 1166 |

  A renewal costs about 60 µs more with the lease in the index, whatever the lane holds; a claim costs more in proportion to the held runs without it: about 1 ms at H = 2000, the extra of some 20 renewals, which is one run of about 200 s at the default 30 s ttl (renewals every 10 s). So which is cheaper depends on run length and lane size; but the claim cost grows with the lane, and the dispatcher waits on it, while the renewal cost does not. Kept as is: claims stay flat as lanes fill, and the order is the scope's. Revisit if renewals dominate write volume (long runs, short ttl): the fix then is a longer ttl, not a different index. The index is 9.4 MB against 7.8 MB after the renewals (more dead index tuples until vacuum).
- **`store.WithLease(owner, ttl)`**: a run created with it is claimed in the same write. `Runner.Run`, `RunFlow` and `Enqueue` use it, so no dispatcher can take a new run between its `Create` and its `Claim` (one write fewer too). It replaces the recovery loop's rule that skipped runs younger than a lease. (Creating with `start_at = now + ttl` instead was rejected: it would delay a run's retries by a ttl after an early failure.)
- **`ClaimNext` returns `store.ErrNotFound`** when nothing of those lanes is due. Lanes are deduplicated; an empty list, an empty owner or a ttl ≤ 0 is `ErrInvalid`. Priority is an `int32` (`MinPriority`..`MaxPriority`), the Postgres column's range.
- **The dispatcher is still `Recovery`** (the name in the API and in T104's notes), with `WithLanes` (default `[""]`). It claims only while a worker slot is free, so a run is never claimed and then left waiting for a worker.
  - A run claimed twice in one pass (its execution ended at once and left it due) keeps the second claim's lease as a one-ttl backoff, is counted as skipped, and the pass goes on. (Review: it used to be released and end the pass, so such a run at the head of a lane was the only run of the lane the dispatcher ever ran, pass after pass; with one worker the runs behind it never ran.)
  - On a resume error it keeps the claim's lease, so the run backs off one ttl and the runs behind it are not blocked. A test that resumes by hand after such an error now expires the lease first (`flow_test.go`). It cannot strand a run (review): nothing renews that lease (the renewal starts only with an execution), it ends one ttl after the claim, and the run is then due again (`Due` is the latest of three finite times, and the run stays queued); a failure after the claim inside `resume` releases at once instead. The cost: for that ttl, `Cancel` and a manual `Resume` of the run get `store.ErrLeased`, so a run that can never resume is cancelable only between its backoffs, as before for a run held by a crashed process.
- **`Runner.Enqueue(ctx, conversationID, msg, opts...)`** creates the run leased, stores the prompt (once), and releases it; the dispatcher resumes it like any run whose transcript ends on a user message. If the prompt cannot be stored the run is canceled. It refuses `Interactive` (an interactive run is never queued). `Run` refuses `StartAt` (`ErrInvalidConfig`): a run it starts runs now.

### For T105

- A background pool is a lane: give each pool a dispatcher, `runner.NewRecovery(r, admin, runner.WithLanes(pool), runner.WithWorkers(capacity))`. It claims (`ClaimNext`) only while it has a free worker slot, which is the pool's capacity.
- Runs go to a pool's lane with `runner.Lane(name)` on `Run` / `Enqueue` (and `runner.Priority(n)`, `runner.StartAt(t)` on `Enqueue`). Storing which pool ran a run is still T105's (see T104's notes).

### Mutation list

Each plant was run against the package named, then restored. All caught.

| Plant | Caught by |
|---|---|
| `backend.Resumable` ignores `StartAt` (memory) | `ListResumableWaitsForTheStartTime`, `TimesOutsideTheStoredRange` |
| SQLite resumable SQL ignores `start_at` | same |
| Postgres resumable SQL ignores `start_at` | same (integration) |
| `ClaimOrder` takes the lowest priority first | `ClaimNextTakesPriorityThenDueTime`, `ClaimNextKeepsLanesApart` |
| memory `ClaimNext` ignores lanes | `ClaimNextKeepsLanesApart`, run `Simulation` |
| interactive runs claimable | `ClaimNextNeverTakesARunThatIsNotDue`, `Simulation` |
| SQLite claim ignores the due order | `ClaimNextTakesPriorityThenDueTime`, `Simulation` |
| SQLite claim ignores the lease (due = start time) | `ClaimNextNeverTakesARunThatIsNotDue`, `ClaimNextLeasesTheRun`, `ClaimersNeverGetTheSameRun`, … |
| Postgres due ignores the lease | `ClaimNextLeasesTheRun`, `ClaimNextNeverTakesARunThatIsNotDue`, `ClaimersNeverGetTheSameRun` (integration) |
| Postgres claim without `FOR UPDATE SKIP LOCKED` | `ClaimersNeverGetTheSameRun` (integration) |
| Postgres claim takes the lowest priority across lanes | `ClaimNextKeepsLanesApart`, `Simulation` (integration) |
| SQLite migration without the backfill | `TestRunsFromBeforeTheQueueAreQueued` |
| Postgres migration without lifting forced RLS | `TestRunsFromBeforeTheQueueAreQueued` (integration) |
| `CheckCreate` accepts any priority | `QueueOptionsAreStored` |
| `CheckCreate` ignores the start time | `ListResumableWaitsForTheStartTime`, `QueueOptionsAreStored` |
| SQLite `Create` drops the lease | `CreateWithALease`, `Simulation` |
| Postgres `Create` stores no lane | `QueueOptionsAreStored`, `Simulation` (integration) |
| dispatcher claims a run again and again in a pass; or ends the pass at it (review) | `TestDispatcherRunsARunOncePerPass` |
| dispatcher ignores its lanes | `TestDispatcherTakesItsLanesByPriority` |
| dispatcher releases a run it cannot resume | `TestDispatcherBacksOffARunItCannotResume` |
| `Enqueue` keeps its lease | `TestDispatcherTakesItsLanesByPriority`, `…BacksOff…`, `…OncePerPass` |
| `Enqueue` stores no prompt | `TestEnqueueCancelsARunWhosePromptIsNotStored`, dispatcher tests |
| `Enqueue` leaves a run whose prompt failed | `TestEnqueueCancelsARunWhosePromptIsNotStored` |
| `Run` creates its run unleased | `TestRecoveryLeavesANewRunToItsCreator` |
| `Run` accepts `StartAt` | `TestEnqueueRefusals` |

The plant "SQLite claim ignores the lease" first hung `ClaimNextNeverTakesARunThatIsNotDue` (it claimed the same run forever); the test now stops after five claims, so it fails instead.

### Test time

`pkg/store/sqlite` under `-race` is near `go test`'s 10-minute default on a loaded machine (load average about 27). It passed alone in 596 s, and timed out twice inside `make check` / `make integration`, in tests T099 does not touch (such as `RacingWritersExactlyOneWins`). Its Admin and RunStore suites, run side by side, took 454 s on main and 552 s on this branch. Most of the difference is in `LimitAboveTheMaximumIsLowered`, which already existed (one more index and three more columns per insert), plus about 16 s of new claim tests. Postgres is unaffected (259 s in the integration tier, which passed).
