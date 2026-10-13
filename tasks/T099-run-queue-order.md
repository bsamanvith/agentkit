---
id: T099
title: "runs: start time, lane and priority, and claiming the next run in that order"
epic: E05
status: todo
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

- [ ] `storetest`: claim order (priority, then due time) within a lane; lanes isolated; a run not yet due never claimed; two claimers never get the same run. Covered on all three stores, Postgres in the integration tier.
- [ ] Runner: a scheduled run starts at its time, through the dispatcher; its prompt is stored once.
- [ ] Migration 0006 backfills existing runs, and leaves them claimable as before.
- [ ] Benchmark: `ClaimNext` under parallel claimers on Postgres; the plan uses the index (`EXPLAIN`).
- [ ] Mutation list recorded.
