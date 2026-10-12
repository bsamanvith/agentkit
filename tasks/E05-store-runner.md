---
id: E05
title: Store, runner and compaction (M4)
status: in-progress
---

# E05: Store, runner and compaction

Runs and conversations persist and resume. Direction: [PLAN.md `store`](../docs/PLAN.md#store) and the T022 "Contract for the store layer" ([T022](T022-harness-loop.md)). Testing: every task here has concurrency or hostile input, so ENGINEERING §2.6 applies (invariants checked after every run through one helper, a seeded simulation where paths interact, mutation lists recorded).

## Decisions (made in planning; change them in the task that finds them wrong)

| Question (from the T022 handoff) | Decision |
|---|---|
| The interfaces | `pkg/store` defines three small interfaces: `ConversationStore` (an append-only log of entries per conversation, with history and context reads), `RunStore` (run metadata plus a lease per run), and `flow.Checkpointer` (flow's own interface, implemented by the store, not redefined). Every method is tenant-scoped through `ctx` (`pkg/tenancy`). |
| Who turns events into entries | A new package **`pkg/runner`**: it runs a `harness.Harness` with persistence. It loads the context, runs, writes the prompt entry first and one entry per `EventTurnDone`, keeps the in-process context cache, holds the run lease, and compacts. `harness` stores nothing and `store` never imports `harness`. |
| SQLite and Postgres schemas | SQLite in this epic (T029, done), with a pure-Go driver so the image stays static, for local and single-node use. **Postgres is the server backend** (owner, 2026-10-12: Postgres + Redis), in [E18](E18-streaming-backend.md) T088, passing the same suites; row-level security later. |
| When to compact | Both: **reactively** when a run fails with `llm.ErrContextTooLong` (compact, then resume), and **proactively** after a run when the last turn's prompt size (input + cache read + cache write tokens) passed a configured share of the model's context window. Compaction appends an entry; it never edits or deletes. |
| One writer per run | A lease in `RunStore` (owner, expiry, renewal), taken by the runner before running or resuming. `flow.Checkpointer` stays as it is. |
| Agent step transcripts (T024 handoff) | The store-backed saver is the conversation log: transcripts are **not deleted** when a run finishes (retention is a later per-tenant policy), and agent step output holds a **reference** to the conversation instead of the whole transcript, so checkpoints stay small. Both in T030. |
| When a stopped run continues | Four cases: an **approval** (resumed by a human decision), a **crash or deploy** (found and resumed by the runner's recovery loop), a **transient failure** (retried by the runner with backoff, up to a limit), a **context that is too long** (compacted, then resumed, T031). A completed, rejected or canceled run is final and never resumed. Run statuses and the queries that make this possible are in `RunStore` (T028); the policy is in the runner (T030). |
| A new message while a run is unfinished | A conversation has at most one unfinished run. A new message is refused with `ErrRunUnfinished` until that run is resumed to the end or canceled. Canceling appends a marker assistant message when the conversation ends on a user message, so it stays valid (append-only, nothing edited). |
| Recovery across tenants (T028 handoff) | Every store method is tenant-scoped, but the recovery loop runs with no request and no tenant. A separate **`store.Admin`** interface, implemented by the memory and SQLite stores, has `ListResumableAll(ctx, now, limit)` returning `(tenant, run)` pairs. It is only ever given to the runner's recovery loop, never to request-handling code, and the runner then resumes each run with that tenant in `ctx`. Added in T038 (interface, memory store, suite), implemented for SQLite in T029, used in T030. |
| Resuming a changed step tree | A shape fingerprint in flow checkpoints (T032), required now that checkpoints outlive deployments. |

## Contracts from T019, T027 and T032 (binding for T028, T029, T030)

From the groundwork handoff; details in those task files' "For T028/T030" Notes.

- **Tenancy.** Every store method starts with `tenant, err := tenancy.Require(ctx)` and returns that error unchanged; `tenant.ID()` is in every key and every `WHERE` clause. Suites check "no tenant → `ErrNoTenant`" and "tenant A cannot touch tenant B".
  - `tenancy.With` on the same tenant starts a fresh scope and drops an earlier user or request ID (by design): callers re-asserting a tenant call `WithUser`/`WithRequest` after it.
  - Tenant IDs are case-sensitive: never use a raw ID as a file name (case-insensitive file systems).
- **Checkpoint shape.** Persist `flow.Checkpoint.Shape` (SQLite: `shape TEXT NOT NULL DEFAULT ''`); an empty value stays empty. `flowtest.RunCheckpointer` fails a store that loses it.
- **Leases and time.** Leases take a `clock.Clock` (`WithClock`, default `clock.Real()`); expiry is `clock.Now().Add(ttl)`. Compare `Now()` with a deadline; never count ticks (a ticker holds one tick).
  - Tests: build `clocktest.NewFake`, start the code, `WaitForTimers(ctx, n)` before `Advance`, one interval at a time (`Advance` is one step). The fake is for tests only (abandoned `After` timers stay in its waiting set).

## Tasks

- [x] [T019](T019-clock.md) `pkg/clock` and fake clock (from E01; the runner and store need it, and the harness switches from `WithNow`)
- [x] [T027](T027-tenancy.md) `pkg/tenancy`: tenant in `ctx`
- [x] [T028](T028-store-interfaces.md) `pkg/store`: interfaces, in-memory store, `storetest` conformance suites
- [x] [T038](T038-store-admin.md) `store.Admin`: the one cross-tenant read, for recovery (small; unblocks T029 and T030 in parallel)
- [x] [T029](T029-store-sqlite.md) `pkg/store/sqlite`: schema, migrations, all suites
- [x] [T030](T030-runner.md) `pkg/runner`: run agents with persistence (entries, context, cache, lease, resume)
- [ ] [T031](T031-compaction.md) Compaction in the runner (reactive and proactive)
- [x] [T032](T032-flow-shape-fingerprint.md) `flow`: detect a changed step tree on resume
- [ ] [T033](T033-harness-clock.md) harness: use `pkg/clock` instead of `WithNow` (small; any time)

```
T019 ─────────────────┐
T027 ── T028 ─┬─ T029 │
              └───────┴─ T030 ── T031
T032 (independent)
```

T019, T027 and T032 can start at once, in parallel. T029 and T030 can run in parallel after T028.
