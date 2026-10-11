---
id: T027
title: "pkg/tenancy: tenant in ctx"
epic: E05
status: todo
depends_on: [T001]
---

# T027: `pkg/tenancy`

## Context

Multi-tenant in v1 ([PLAN.md `tenancy`](../docs/PLAN.md#tenancy)): tenant, user and request scope travel in `context.Context`, set once by middleware. The store (T028) scopes every read and write by it. Keep it tiny.

## Scope

- `tenancy.Tenant` (ID, validated: non-empty, bounded, safe as a key), `tenancy.With(ctx, t)`, `tenancy.From(ctx) (Tenant, bool)`, `tenancy.MustFrom` for code paths where a missing tenant is a bug, and `ErrNoTenant`.
- Optional user and request IDs in the same scope.

## Acceptance criteria

- [ ] `From` returns what `With` stored; nested `With` overrides; a context without a tenant reports false.
- [ ] Invalid tenant IDs are rejected by the constructor.
- [ ] Values survive `context.WithCancel`/`WithTimeout` and are not visible to sibling contexts.

## Notes

- stdlib only.
