---
id: T093
title: "Postgres row-level security by tenant: a second isolation layer under the store's own scoping"
epic: E18
status: todo
depends_on: [T088]
---

# T093: Postgres row-level security

## Context

The store already scopes every query by tenant (E05). Row-level security makes Postgres enforce the same rule, so a missed `WHERE tenant = ...` returns nothing instead of another tenant's rows. Proposed in the T029 handoff; [PLAN `tenancy`](../docs/PLAN.md#tenancy).

## Scope

- **Policies:** `ENABLE` and `FORCE ROW LEVEL SECURITY` on every tenant table, with a policy `tenant = current_setting('app.tenant')`.
- **Setting the tenant:** every transaction sets `SET LOCAL app.tenant` from `tenancy.From(ctx)`. A missing tenant is an error before any query runs.
- **Roles:**
  - the application role is not the table owner and cannot bypass RLS;
  - `store.Admin` (T038) runs on a separate role with `BYPASSRLS`, from its own URL (`AGENTKIT_STORE_ADMIN`);
  - migrations run as the owner.
- **Pooling:** `SET LOCAL` only, never a session-level `SET`, so a pooled connection never carries a tenant into the next transaction.

## Acceptance criteria

- [ ] **Policy test:** a query planted without its tenant filter returns no rows from another tenant.
- [ ] **Pooled connections:** after tenant A's transaction, a reused connection running without a tenant fails; it never sees A's rows.
- [ ] **Admin:** `store.Admin` lists across tenants on its role; the application role cannot.
- [ ] **Suites:** all store suites still pass with RLS on.
- [ ] **Mutation list** recorded.

## References

- [Postgres row security policies](https://www.postgresql.org/docs/current/ddl-rowsecurity.html)
- [`SET LOCAL`](https://www.postgresql.org/docs/current/sql-set.html)
