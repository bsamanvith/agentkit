---
id: T038
title: "store.Admin: the one cross-tenant read, for recovery"
epic: E05
status: todo
depends_on: [T028]
---

# T038: `store.Admin`

## Context

Every store method is tenant-scoped, but the runner's recovery loop (T030) runs with no request and no tenant (T028 handoff). Planner decision in [E05](E05-store-runner.md): a separate interface with the one cross-tenant read, given only to the recovery loop. Split out of T029 so that T029 (SQLite) and T030 (runner) can run in parallel.

## Scope

- `store.Admin` with `ListResumableAll(ctx, now, limit) ([]TenantRun, error)`: resumable runs (same rule as `RunStore.ListResumable`) of every tenant, each with its tenant, ordered by `CreatedAt` then tenant then ID. Needs no tenant in `ctx`.
- Not reachable through `ConversationStore` or `RunStore` (a separate interface; the memory store's admin view is a separate value).
- `storetest.RunAdmin` and the `store/memory` implementation. T029 implements it for SQLite.

## Acceptance criteria

- [ ] Returns exactly the resumable runs of all tenants, each with the right tenant; not live-leased, suspended, final or not-yet-due runs.
- [ ] Ordering is stable and the limit is honored.
- [ ] A tenant-scoped `RunStore` value cannot be used to list other tenants (type-level: the admin view is a different value).
- [ ] Memory store passes `storetest.RunAdmin`; mutation list recorded.
