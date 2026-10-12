---
id: T039
title: "pkg/auth: principals, API-key authenticator, edge middleware"
epic: E07
status: todo
depends_on: [T027]
---

# T039: `pkg/auth`

## Context

Every request should carry a tenant (E05 scopes all data by it), but today nothing authenticates callers beyond one static token on `/v1`. This task builds the reusable part: the middleware and the API-key authenticator. T040 wires it into `agentkit serve`. Decisions: [E07](E07-auth-credentials.md).

## Scope

- **`auth.Principal`**: `Tenant tenancy.Tenant`, `User string`, `Scopes []string`, `Toolsets []string` (names or `path.Match` globs; empty means none).
  - `p.AllowsToolset(name) bool` and `p.HasScope(s) bool`.
  - `auth.From(ctx) (Principal, bool)`.
- **`auth.Authenticator`** interface: `Authenticate(r *http.Request) (Principal, error)`.
  - Errors: `ErrNoCredentials` (nothing presented) and `ErrInvalidCredentials` (presented and wrong).
  - Any other error is internal: it answers 500 and is logged without the credential.
- **`auth.Middleware(a Authenticator, opts...) func(http.Handler) http.Handler`**:
  - On success it puts the principal in `ctx`, plus `tenancy.With`, `WithUser` and `WithRequest`. The request ID comes from a header if it is valid, otherwise it is generated.
  - On failure it answers 401 with `WWW-Authenticate: Bearer` (realm, plus `error="invalid_token"` when a token was presented).
  - The failure body is an option, so `/v1` keeps its JSON error shape and MCP gets a plain one.
  - An option adds `resource_metadata` to the header (used by T041).
- **`auth.APIKeys`**, an Authenticator built from config entries `{id, hash, tenant, user?, scopes, toolsets}`:
  - `hash` is `sha256:<hex>` of the key; the key itself is never in config.
  - A key is read from `Authorization: Bearer <key>`.
  - Comparison is constant time. Lookup is by hash, and no early exit reveals which entry matched.
  - Entry IDs appear in logs; keys never do.
  - `auth.HashKey(key) string` helper.
  - Keys shorter than 16 bytes are rejected when the entries are built.
- **`auth.Chain(authenticators...)`**: tries each in order. `ErrNoCredentials` moves to the next; `ErrInvalidCredentials` stops. This lets T041 add JWT beside API keys.
- **`authtest`**: a fake authenticator, and `RunAuthenticator`, a conformance suite every authenticator passes (T041 reuses it).

## Out of scope

- Wiring into `cmd/agentkit` and the agent API (T040). JWT (T041). Rate limiting per principal (later).

## Acceptance criteria

- [ ] A valid key gives a principal whose tenant, user, scopes and toolsets are exactly those configured, and the handler sees them through `auth.From`, `tenancy.From` and `tenancy.User`.
- [ ] A missing key, a wrong key, a key differing in one byte, a key prefix, a key with a different scheme (`Basic`, no scheme, lowercase `bearer` is accepted per RFC 6750), several `Authorization` headers, and an oversized header all give 401 with the right `WWW-Authenticate`. The handler is never called.
- [ ] Two keys for two tenants never cross: a request with A's key can only produce tenant A, whatever other headers say (a client-sent tenant or user header is ignored).
- [ ] Toolset globs: `AllowsToolset` matches names and globs, and empty allows nothing (table test).
- [ ] `Chain`: no credentials falls through; invalid credentials stop; the first success wins.
- [ ] Logging: with a canary key, no log line, error string or response body contains the key or its hash (handler-side log capture).
- [ ] Config errors: a duplicate ID, a duplicate hash, a malformed hash, an invalid tenant, or a short key fails construction, with a message naming the entry ID.
- [ ] `FuzzAuthorizationHeader`: never panics, never authenticates without a configured key.
- [ ] Mutation list recorded.

## Notes

- Don't put `Principal` in `pkg/tenancy`. Tenancy is labels only ("this package does not authenticate anyone"); auth depends on tenancy, never the reverse.
