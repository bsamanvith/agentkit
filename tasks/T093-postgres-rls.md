---
id: T093
title: "Postgres row-level security by tenant: a second isolation layer under the store's own scoping"
epic: E18
status: done
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

- [x] **Policy test:** a query planted without its tenant filter returns no rows from another tenant.
- [x] **Pooled connections:** after tenant A's transaction, a reused connection running without a tenant fails; it never sees A's rows.
- [x] **Admin:** `store.Admin` lists across tenants on its role; the application role cannot.
- [x] **Suites:** all store suites still pass with RLS on.
- [x] **Mutation list** recorded.

## References

- [Postgres row security policies](https://www.postgresql.org/docs/current/ddl-rowsecurity.html)
- [`SET LOCAL`](https://www.postgresql.org/docs/current/sql-set.html)

## Notes

- Done 2026-10-17. Decisions:
  - **The tenant is set when a connection leaves the pool**, not with `SET LOCAL` in a transaction around each query. pgxpool's `PrepareConn` hook runs `set_config('app.tenant', <tenant>, false)` before every acquisition, from the acquiring context, and overwrites whatever the connection's previous user set. A context with no tenant fails in the hook, before its query is sent. Every store query goes through the pool, so this covers all of them with one round trip per acquisition and no change to the store's queries; `SET LOCAL` would have wrapped every single statement in a transaction (three more round trips).
  - The policy compares with `current_setting('app.tenant', true)`, so a session that never set it sees nothing rather than erroring.
  - Migration 0008 enables and FORCEs RLS on all six tenant tables. Migrations and `Admin` run with an internal "unscoped" context (`app.tenant` = '', which matches no row). A future data migration that rewrites rows must run as a role that bypasses RLS.
  - `postgres.WithAdminURL` / `AGENTKIT_STORE_ADMIN` gives `Admin` its own pool. `Admin` checks its role (`rolsuper OR rolbypassrls`) on every pass and fails with `ErrAdminRole` instead of silently listing nothing. `serve` probes it once at startup and fails naming `AGENTKIT_STORE_ADMIN`.
  - Without an app role (the CI service container, the local k8s overlay) everything runs as a superuser, which bypasses RLS. That keeps working as before, unprotected. The store's own integration tests now run every suite as a non-superuser role, and CI runs the agent API's Postgres suite as a role that owns its schema (so FORCE applies).
- Mutations (each failed the integration tests): RLS removed from `runs`; an open (`USING (true)`) policy on each of the six tables in turn; no write check on `runs`; a missing tenant running as ''; the admin role check removed; `Admin` on the application pool. One equivalent mutation: skipping the overwrite for unscoped contexts. Only migrations (no RLS tables) and `Admin` (which needs a bypassing role anyway) run unscoped.
