---
id: E05
title: Store, runner and compaction (M4)
status: todo
---

# E05: Store, runner and compaction

Runs and conversations persist and resume. Direction: [PLAN.md `store`](../docs/PLAN.md#store) and the T022 "Contract for the store layer" ([T022](T022-harness-loop.md)). Testing: every task here has concurrency or hostile input, so ENGINEERING §2.6 applies (invariants checked after every run through one helper, a seeded simulation where paths interact, mutation lists recorded).

## Decisions (made in planning; change them in the task that finds them wrong)

| Question (from the T022 handoff) | Decision |
|---|---|
| The interfaces | `pkg/store` defines three small interfaces: `ConversationStore` (an append-only log of entries per conversation, with history and context reads), `RunStore` (run metadata plus a lease per run), and `flow.Checkpointer` (flow's own interface, implemented by the store, not redefined). Every method is tenant-scoped through `ctx` (`pkg/tenancy`). |
| Who turns events into entries | A new package **`pkg/runner`**: it runs a `harness.Harness` with persistence. It loads the context, runs, writes the prompt entry first and one entry per `EventTurnDone`, keeps the in-process context cache, holds the run lease, and compacts. `harness` stores nothing and `store` never imports `harness`. |
| SQLite and Postgres schemas | SQLite in this epic (T029), with a pure-Go driver so the image stays static. Postgres comes with tenancy and row-level security in a later epic; the schema is written so it ports column for column. |
| When to compact | Both: **reactively** when a run fails with `llm.ErrContextTooLong` (compact, then resume), and **proactively** after a run when the last turn's prompt size (input + cache read + cache write tokens) passed a configured share of the model's context window. Compaction appends an entry; it never edits or deletes. |
| One writer per run | A lease in `RunStore` (owner, expiry, renewal), taken by the runner before running or resuming. `flow.Checkpointer` stays as it is. |
| Resuming a changed step tree | A shape fingerprint in flow checkpoints (T032), required now that checkpoints outlive deployments. |

## Tasks

- [ ] [T019](T019-clock.md) `pkg/clock` and fake clock (from E01; the runner and store need it, and the harness switches from `WithNow`)
- [ ] [T027](T027-tenancy.md) `pkg/tenancy`: tenant in `ctx`
- [ ] [T028](T028-store-interfaces.md) `pkg/store`: interfaces, in-memory store, `storetest` conformance suites
- [ ] [T029](T029-store-sqlite.md) `pkg/store/sqlite`: schema, migrations, all suites
- [ ] [T030](T030-runner.md) `pkg/runner`: run agents with persistence (entries, context, cache, lease, resume)
- [ ] [T031](T031-compaction.md) Compaction in the runner (reactive and proactive)
- [ ] [T032](T032-flow-shape-fingerprint.md) `flow`: detect a changed step tree on resume

```
T019 ─────────────────┐
T027 ── T028 ─┬─ T029 │
              └───────┴─ T030 ── T031
T032 (independent)
```

T019, T027 and T032 can start at once, in parallel. T029 and T030 can run in parallel after T028.
