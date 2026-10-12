---
id: T038
title: "store.Admin: the one cross-tenant read, for recovery"
epic: E05
status: done
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

- [x] Returns exactly the resumable runs of all tenants, each with the right tenant; not live-leased, suspended, final or not-yet-due runs. (A `ListsTheResumableRunsOfEveryTenant`: three tenants, the same run and conversation ids in two of them, one run per case (live lease, expired lease, never claimed, released, suspended, due, due at `now`, due 1 ns after `now`, failed for good, each final status), a shared id resumable in both tenants as two different runs, an id resumable in one tenant only; read at `now - 1ns`, `now`, `now + 1ns` and `now + 2h` while the store's clock stays at `now`. A `NeedsNoTenantAndIgnoresOne`, A `CancelledContextIsReported`. Run invariant 7 after every test, and A `Simulation` against a model at random steps)
- [x] Ordering is stable and the limit is honored. (A `OrderIsStableAndLimitIsHonored`: the oldest run in the tenant that sorts last, five runs created at one instant in an order that is neither the tenants' nor the ids', the newest in the tenant and id that sort first; every limit from 1 to n+1, 0 is the default page size (107 runs), negative is `ErrInvalid`, a repeated call returns the same runs)
- [x] A tenant-scoped `RunStore` value cannot be used to list other tenants (type-level: the admin view is a different value). (memory `TestAdminIsASeparateValue`: the store held as a `RunStore` is not a `store.Admin`, and the Admin view is not a `RunStore`)
- [x] Memory store passes `storetest.RunAdmin`; mutation list recorded. (memory `TestAdmin`, `FuzzAdminSimulation`, `ExampleStore_Admin`; Notes below)

## Notes

### API

- `pkg/store`:
  - `type TenantRun struct { Tenant tenancy.Tenant; Run Run }`
  - `type Admin interface { ListResumableAll(ctx context.Context, now time.Time, limit int) ([]TenantRun, error) }`
- `pkg/store/memory`: `func (s *Store) Admin() store.Admin`. It returns an unexported `admin{s}` over the same data. `*Store` has no `ListResumableAll`.
- `pkg/store/storetest`:
  - `func RunAdmin(t *testing.T, newStore func(t *testing.T, clk clock.Clock) (store.RunStore, store.Admin))`. The factory returns both views of one empty store.
  - `func SimulateAdmin(t *testing.T, newStore func(t *testing.T, clk clock.Clock) (store.RunStore, store.Admin), seed uint64)`.

### Design decisions

- **The rule** is `backend.Resumable`, the one `ListResumable` uses: `running` with no lease held at `now`, or `failed` + `Retryable` with `NextAttemptAt <= now`. It uses the `now` passed in, never the store's clock. The limit goes through `backend.Limit` like `ListResumable`: 0 is `DefaultPageLimit`, above `MaxPageLimit` is lowered to it, negative is `ErrInvalid`. There is no cursor (`ListResumable` has none either).
- **Order**: `CreatedAt`, then tenant id compared as bytes (`strings.Compare`), then run ID. CreatedAt comes first so the oldest interrupted work is resumed first whatever its tenant. The memory store's comparator lives in `memory` because `pkg/store/internal/backend` is outside this task's files. If a second backend wants it shared, move it next to `backend.RunKey`.
- **ctx**: a done context gives its error. No tenant is required, and a tenant that is there is ignored: the recovery loop may well run under some tenant's context.
- **Type-level separation**: `store.Admin` is its own interface, and the memory store hands it out as a different value. Code that holds the store as a `RunStore`, `ConversationStore` or `flow.Checkpointer` cannot type-assert its way to other tenants' runs. The admin value has no other method, so the recovery loop cannot write through it either.
- `pkg/store` now imports `pkg/tenancy` (already on the archtest allowlist).

### Invariant 7 (checked when every RunAdmin test ends, through the run rig)

The run rig takes an optional `Admin` (`newAdminRig`; `RunRunStore` passes none). With one, after the six run invariants, `ListResumableAll` at the clock's time, with **no tenant** in `ctx`, must equal the union of every tenant's `ListResumable` at the same time, each run with its tenant, re-sorted by the admin order (restated in storetest). It is compared on the tenant and every run field (`sameRun`), not on ids, since ids collide across tenants. Invariant 6 ties each `ListResumable` to the rule, so this ties the cross-tenant read to the same rule, and T029 cannot drift between the two queries.

`SimulateAdmin` is `SimulateRuns` with that rig. At every read step it also compares `ListResumableAll`, called with a tenant in `ctx` that must be ignored and at a time within ±2 s of the clock, against the model of both tenants, sorted by (creation time, tenant, id). Runs of the two tenants are often created at the same instant, so the tenant tie-break is exercised.

### Gates

- `make check` exit 0.
- `go test -race -shuffle=on -count=20 ./pkg/store/...` clean.
- `make fuzz FUZZTIME=60s` clean, including the new `FuzzAdminSimulation`.

### Mutation check

Each bug was planted alone in `pkg/store/memory/memory.go` by a script, then `go test -race -count=1 ./pkg/store/...` was run and the file restored from a backup (checked with `cmp`). 20 plants, all compiled, none survived. A = `TestAdmin` (`storetest.RunAdmin`); Sim = `TestAdmin/Simulation` and the `FuzzAdminSimulation` seed corpus.

| Planted bug | Failing tests |
|---|---|
| Ignores the lease (running runs with a live lease returned) | A `ListsTheResumableRunsOfEveryTenant` and every A test (invariant 7), Sim |
| Reads the store's clock instead of `now` | A `ListsTheResumableRunsOfEveryTenant`, Sim |
| Tenant tie-break dropped | A `OrderIsStableAndLimitIsHonored`, `ListsTheResumableRunsOfEveryTenant`, `NeedsNoTenantAndIgnoresOne`, Sim |
| Run ID compared before tenant | A `OrderIsStableAndLimitIsHonored`, Sim |
| Tenant compared before `CreatedAt` | A `OrderIsStableAndLimitIsHonored`, `ListsTheResumableRunsOfEveryTenant`, Sim |
| Limit off by one (one too many) | A `OrderIsStableAndLimitIsHonored` |
| Limit off by one (one too few) | A `OrderIsStableAndLimitIsHonored` |
| A zero limit means no limit | A `OrderIsStableAndLimitIsHonored` |
| A negative limit accepted | A `OrderIsStableAndLimitIsHonored` |
| Wrong tenant for a run id in two tenants (tagged with the first tenant having the id) | A `ListsTheResumableRunsOfEveryTenant`, `OrderIsStableAndLimitIsHonored`, `NeedsNoTenantAndIgnoresOne`, memory `ExampleStore_Admin` |
| Right tenant, wrong run for an id in two tenants (read from the first tenant having it) | A `ListsTheResumableRunsOfEveryTenant`, `OrderIsStableAndLimitIsHonored` (invariant 7) |
| `Retryable` skipped | A `ListsTheResumableRunsOfEveryTenant`, Sim |
| Failed runs not yet due returned | A `ListsTheResumableRunsOfEveryTenant`, Sim |
| Suspended runs returned | A `ListsTheResumableRunsOfEveryTenant`, Sim |
| Final runs returned | A `ListsTheResumableRunsOfEveryTenant` and every A test (invariant 7), Sim |
| A tenant in `ctx` filters the list | A `NeedsNoTenantAndIgnoresOne`, Sim |
| A tenant in `ctx` required | every A test, Sim, memory `ExampleStore_Admin` |
| No `ctx` check | A `CancelledContextIsReported` |
| Only one tenant's runs listed | A `ListsTheResumableRunsOfEveryTenant`, `OrderIsStableAndLimitIsHonored`, `NeedsNoTenantAndIgnoresOne`, Sim, memory `ExampleStore_Admin` |
| `*memory.Store` itself implements `store.Admin` (a delegating method) | memory `TestAdminIsASeparateValue` |

Not plantable: the admin view sharing `Pending` with the store. Resumable runs are running or failed, and `Transition` clears `Pending` on every move but to suspended, so a listed run never has any. The view still returns `cloneRun` copies.

### For T029 (SQLite)

- **A separate admin value**, as in memory: `func (s *Store) Admin() store.Admin { return admin{s} }` with `type admin struct{ s *Store }`. Never put `ListResumableAll` on `*Store`. Run `storetest.RunAdmin(t, func(t *testing.T, clk clock.Clock) (store.RunStore, store.Admin) { s := open(t, clk); return s, s.Admin() })`, add a test like memory's `TestAdminIsASeparateValue`, and consider a fuzz target over `storetest.SimulateAdmin`.
- **The query** is `ListResumable`'s without the tenant filter, plus the tenant column:

  ```sql
  SELECT tenant, <run columns> FROM runs
  WHERE (status = 'running' AND (lease_owner = '' OR lease_expires <= :now))
     OR (status = 'failed' AND retryable AND next_attempt_at <= :now)
  ORDER BY created_at, tenant, id
  LIMIT :limit
  ```

  `:limit` comes from `backend.Limit(store.Page{Limit: limit})`, and `:now` is the argument, never the store's clock. Check `ctx.Err()` first and do not call `tenancy.Require`. Rebuild each tenant with `tenancy.NewTenant` from the stored id; it cannot fail for an id the store accepted, but return the error rather than panic.
- **Index**: `CREATE INDEX runs_resumable ON runs (created_at, tenant, id) WHERE status IN ('running', 'failed')`. SQLite walks it in output order, filters on the lease and retry columns and stops at the limit, with no sort. Suspended and final runs, the bulk of the table over time, are not in it.
- **Order must match Go's**: `tenant` and `id` use the default `BINARY` collation (bytewise, as `strings.Compare`), never `NOCASE`. `created_at` must be an integer with full precision. Note that the suites' fake clock starts at the zero `time.Time` (year 1), outside the range of `UnixNano`, so pick an encoding that round-trips it.

### For T030 (runner)

- **Getting the Admin**: the store's `Admin()` (memory now, SQLite in T029), called once in the wiring (`cmd/`) and passed only to the recovery loop's constructor. Request-handling code gets the `RunStore` and `ConversationStore` and never the Admin. `pkg/store` can't enforce this; the wiring has to.
- **Calling it**: `ListResumableAll(ctx, clk.Now(), limit)` with the runner's injected clock. `ctx` needs no tenant. For each `TenantRun`, `ctx := tenancy.With(ctx, tr.Tenant)` (a fresh scope; add `WithRequest` after it if logging wants one), then `Claim(tr.Run.ID, owner, ttl)` and resume. The claim decides who resumes: `ErrLeased` means another process got there first, and `ErrFinal` means it finished in between. Skip both.
- **Ordering guarantee**: oldest `CreatedAt` first across all tenants, ties by tenant id and then run ID. The result is deterministic, and two calls at the same `now` with no writes in between return the same runs. There is no cursor. A running run drops off the list once claimed (its lease is held). A failed run does not, because the rule for failed runs ignores the lease: it drops off when the loop moves it `failed → running` after claiming it. Calling again after resuming a batch therefore moves forward. A run the loop leaves alone stays at the head. With a small limit, a run the loop keeps skipping starves the ones behind it, so claim it or move it out of the resumable states (for example to `failed` without `Retryable`).
