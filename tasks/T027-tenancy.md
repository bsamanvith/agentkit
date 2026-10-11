---
id: T027
title: "pkg/tenancy: tenant in ctx"
epic: E05
status: done
depends_on: [T001]
---

# T027: `pkg/tenancy`

## Context

Multi-tenant in v1 ([PLAN.md `tenancy`](../docs/PLAN.md#tenancy)): tenant, user and request scope travel in `context.Context`, set once by middleware. The store (T028) scopes every read and write by it. Keep it tiny.

## Scope

- `tenancy.Tenant` (ID, validated: non-empty, bounded, safe as a key), `tenancy.With(ctx, t)`, `tenancy.From(ctx) (Tenant, bool)`, `tenancy.MustFrom` for code paths where a missing tenant is a bug, and `ErrNoTenant`.
- Optional user and request IDs in the same scope.

## Acceptance criteria

- [x] `From` returns what `With` stored; nested `With` overrides; a context without a tenant reports false. (`TestFromReturnsWhatWithStored`)
- [x] Invalid tenant IDs are rejected by the constructor. (`TestNewTenantRejectsInvalidIDs`, `FuzzNewTenant`)
- [x] Values survive `context.WithCancel`/`WithTimeout` and are not visible to sibling contexts. (`TestScopeSurvivesDerivedContextsButNotSiblings`)

## Notes

- Standard library only; no archtest change.

### API

- `tenancy.Tenant` (comparable; `ID()`), `NewTenant(id) (Tenant, error)`, `MaxIDLength` (64).
- `With(ctx, Tenant)`, `From(ctx) (Tenant, bool)`, `Require(ctx) (Tenant, error)`, `MustFrom(ctx) Tenant`.
- `WithUser`/`User`, `WithRequest`/`Request`: opaque labels for audit logs, not validated.
- `ErrNoTenant`, `ErrInvalidTenant`.

### Design decisions

- **A valid id** has 1 to 64 bytes, each an ASCII letter, digit, `.`, `_` or `-`, and starts with a letter or digit. That makes it safe as a SQL value, a file or URL path segment and a metric label, and rules out `.` and `..`. Non-ASCII is refused so two ids cannot look alike.
- **`Require` is not in the spec.** A store method must refuse a context with no tenant with an error, not a panic, so T028 needs an error-returning variant next to `MustFrom`. Both are one line over `From`.
- **One immutable scope value in `ctx`.** `With` starts a new scope, so the user and request id of tenant A never carry into tenant B. `With(ctx, Tenant{})` clears the scope instead of being ignored, so a context cannot keep the outer tenant by accident: the store then fails closed with `ErrNoTenant`.
- `Tenant` has no exported fields, so the only way to hold a non-zero one is `NewTenant`; the zero value means "none".
- User and request ids are not validated: they are labels, and bounding untrusted header values is the middleware's job.
- Moving the scope across a process boundary (request metadata, ENGINEERING §1.5) is the middleware's job and not built here; it will use `ID()` and `NewTenant`.

### Mutation check

Each bug was planted alone (the code compiled), `go test -race ./pkg/tenancy` was run, and the code was restored afterwards.

| Planted bug | Failing tests |
|---|---|
| Length limit off by one (65 bytes accepted) | `TestNewTenantRejectsInvalidIDs`, `FuzzNewTenant` |
| Empty id accepted | `TestNewTenantRejectsInvalidIDs`, `FuzzNewTenant` |
| `/` allowed in an id | `TestNewTenantRejectsInvalidIDs`, `FuzzNewTenant` |
| Leading `.` allowed (so `..` is a valid id) | `TestNewTenantRejectsInvalidIDs`, `FuzzNewTenant` |
| Non-ASCII bytes allowed | `TestNewTenantRejectsInvalidIDs`, `FuzzNewTenant` |
| `With` keeps the outer user and request id | `TestWithStartsANewScope` |
| `With(ctx, Tenant{})` keeps the outer tenant | `TestWithStartsANewScope` |
| `From` reports ok with no tenant | `TestFromReturnsWhatWithStored`, `TestRequireAndMustFromRefuseAContextWithNoTenant` and 4 more |
| `Require` returns no error without a tenant | `TestRequireAndMustFromRefuseAContextWithNoTenant`, `TestWithStartsANewScope`, `Example` |
| `MustFrom` does not panic | `TestRequireAndMustFromRefuseAContextWithNoTenant` |

`make fuzz FUZZTIME=60s` on `FuzzNewTenant`: 6.1 million executions, clean.

### Extra tests beyond the criteria

`TestRequireAndMustFromRefuseAContextWithNoTenant` and `TestWithStartsANewScope` guard the fail-closed paths the store will rely on; `Example`.

### For T028 and later

- Every store method starts with `tenant, err := tenancy.Require(ctx)` and returns the error unchanged, so a missing tenant is `ErrNoTenant` everywhere. Put `tenant.ID()` in every key, and in every `WHERE`/primary key in SQLite.
- The conformance suite should cover "no tenant → `ErrNoTenant`" and "tenant A cannot read or write tenant B's data" for each method.
- In tests, build the context with `tenancy.With(ctx, tenant)` and a tenant from `NewTenant("t1")`.
