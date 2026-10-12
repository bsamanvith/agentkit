---
id: E07
title: Auth, tenants at the edge, and credentials for connectors
status: todo
---

# E07: Auth, tenants at the edge, and credentials

Two problems, one epic:

1. **Who is calling** (inbound). Today `/v1` has one static bearer token, and **`/mcp/` has no auth at all** (T036 Notes: "`/mcp/` stays open (internal)"). Every request runs without a tenant. This epic authenticates every request on both surfaces, turns the caller into a principal (tenant, user, what they may use), and puts it in `ctx` through `pkg/tenancy`. Then the store's tenant scoping (E05) means something.
2. **What the tools use** (outbound). A Salesforce, HubSpot or Gmail toolset needs that tenant's (or that user's) credentials at call time. These include API keys, OAuth2 tokens that expire and refresh, and client credentials. They must be stored encrypted, scoped per tenant and user, and handed to tools as short-lived values, never logged or returned.

Direction: [PLAN.md `credentials`](../docs/PLAN.md#credentials), [`tenancy`](../docs/PLAN.md#tenancy) and the MCP section ("Auth, tenant resolution and per-tenant credentials plug in as HTTP middleware"). The hooks already exist:
- `server.WithMiddleware` wraps every MCP path, the listing included, and values it puts in the request context reach the tool's context (T018 Notes).
- `pkg/tenancy` is done (T027).
- The agent API keeps its handler behind one auth check (T036).

Testing: everything here is security code with hostile input, so ENGINEERING §2.6 applies in full. Each task runs these checks:
- **Canary secret.** Seed a recognizable secret value, run the whole path (tool call over MCP and through the agent, errors included), then search every log line, event, error message, MCP response, stored entry and checkpoint for it. Any hit fails.
- **Cross-tenant.** Tenant A's key or token never reaches B's data, credentials or toolsets.
- **Fuzzing.** Every parser of untrusted input is fuzzed: headers, tokens, JWTs, OAuth callbacks.

## Decisions (made in planning; change them in the task that finds them wrong)

| Question | Decision |
|---|---|
| One middleware or two | **One edge middleware, `pkg/auth`, in front of both `/v1` and `/mcp/`.** Same principal, same tenant rules, same 401 behavior on both. We don't use the MCP SDK's `auth.RequireBearerToken`: it requires an expiry on every token, so API keys would not fit, and it covers only MCP. We do reuse the SDK's `oauthex` metadata types and `auth.ProtectedResourceMetadataHandler`. |
| What a principal is | `auth.Principal{Tenant, User, Scopes, Toolsets}`, built by an `Authenticator` from the request. The middleware sets `tenancy.With`, `WithUser` and `WithRequest`, and stores the principal in `ctx`. Handlers below never parse credentials. |
| Kinds of inbound credentials | **API keys first** (T039): config holds only a hash of each key, plus the tenant, user, scopes and toolsets it grants; compared in constant time. **OIDC/JWT bearer tokens second** (T041), verified locally against the issuer's JWKS. We are only an OAuth *resource server*; the identity provider (Auth0, Okta, Google, Keycloak, ...) is the authorization server. We never issue tokens. |
| `AGENTKIT_API_TOKEN` | Kept as shorthand for one API key: tenant `default`, all scopes, all toolsets. Existing setups (the quickstart, T036 tests) keep working. |
| MCP and the spec's authorization | With JWT auth configured, we serve `/.well-known/oauth-protected-resource` (RFC 9728) and send `WWW-Authenticate: Bearer resource_metadata=...` on 401. MCP clients that support it (Claude Code) can then discover the identity provider. With API keys, clients send `Authorization: Bearer` (`claude mcp add --header`). **No token passthrough**: the caller's token is never forwarded to a connector's upstream API; tools use stored credentials (MCP spec security rule). |
| Which toolsets a principal may use | `Principal.Toolsets` (names or globs, empty means none). A toolset that is not allowed answers **404** on `/mcp/{name}` and is absent from the `GET /mcp/` listing, so a principal cannot learn other toolsets exist. The agent API allows only the agent's configured toolsets that are also in `Principal.Toolsets`, so tools are fixed per run. |
| Off-loopback without auth | **Startup fails** when `agent` or `mcp-server` listens off loopback with no authenticator configured. This closes the open-`/mcp/` gap. Health, readiness and version stay open. |
| Credential storage | `pkg/credentials`. A **`Vault`** holds sealed blobs keyed by (tenant, optional user, slot), with a version for compare-and-swap. A **`Sealer`** encrypts with AES-256-GCM: the master key comes from `AGENTKIT_CREDENTIALS_KEY`, there is a key ring with key IDs so keys can rotate, and the tenant, user and slot are bound in as associated data. A blob copied to another tenant or slot therefore fails to decrypt. Backends never see plaintext. Memory backend first (T042), then an encrypted file and SQLite (T043). Vault, AWS Secrets Manager and k8s secrets come when a deployment needs them (owner's deploy decision). |
| How a tool gets a credential | A toolset `Spec` names a **credential slot**, for example `{"credential": {"slot": "salesforce", "scope": "tenant"}}`. The `scope` is `tenant` for a shared org connection or `user` for delegated access such as Gmail. The factory receives a `credentials.Source` that can read **only that slot**. At call time it resolves the tenant and user from `ctx` and returns a short-lived value: an access token, or an `http.Client` that adds auth. A missing credential is `credentials.ErrNotConnected`, which reaches the model as a tool error ("not connected") and the API as a clear code, never a 500. |
| OAuth2 refresh across replicas | Refresh happens once per (tenant, user, slot) in a process (singleflight). The rotated refresh token is written with compare-and-swap. On a version conflict, re-read: another replica has already refreshed, and its token is the one to use. A refresh that fails with `invalid_grant` marks the credential as needing reconnection; it is not retried in a loop. `golang.org/x/oauth2` (already in the module graph through the MCP SDK) does the token exchange. |
| Connecting an account | The agent API gets credential endpoints (T045). `PUT` sets an API-key credential (admin scope). `POST .../connect` starts OAuth2 authorization code + PKCE and returns the provider's URL; a `GET /oauth/callback` finishes it. The `state` is single-use, expires, and is bound to the principal that started it. Listing returns metadata only, never values. |
| JWT library | `github.com/go-jose/go-jose/v4`: one package for JWS, JWT and JWK sets. It allows only the configured asymmetric algorithms (never `none`, never HMAC with a public key). T041 may switch to `golang-jwt/jwt/v5` (already in `go.sum`) plus our own JWKS fetch if that is clearly smaller; it records why. |

## Tasks

Two tracks that can start in parallel, and in parallel with E05: inbound (T039 → T040, T041) and credentials (T042 → T043, T044 → T045).

- [ ] [T039](T039-auth-principals-middleware.md) `pkg/auth`: principals, API-key authenticator, edge middleware
- [ ] [T040](T040-serve-auth-wiring.md) `agentkit serve` behind auth: `/v1` and `/mcp/`, per-principal toolsets, no open MCP off loopback
- [ ] [T041](T041-auth-jwt-oidc.md) JWT/OIDC authenticator and MCP protected resource metadata
- [ ] [T042](T042-credentials-core.md) `pkg/credentials`: secret values, sealer and key ring, vault interface, memory vault, conformance suite
- [ ] [T043](T043-credentials-backends.md) Credential backends: encrypted file now, SQLite on T029
- [ ] [T044](T044-credentials-in-tools.md) Credentials in tools: slots in toolset specs, scoped sources, OAuth2 refresh, the canary test end to end
- [ ] [T045](T045-connect-accounts.md) Connecting accounts: credential API and the OAuth2 authorization code + PKCE flow
- [ ] [T009](T009-kind-cluster.md) kind cluster and the k8s secrets backend (waits for the owner's deploy decision)

## Order and overlaps

- **T039 and T042 start now.** They touch only new packages.
- T040 changes `cmd/agentkit` and `pkg/agentapi`, and so does T037 (agent API v1). Whichever starts second rebases onto the first. T037 takes the tenant from the principal T040 provides; until then it uses `default`.
- T043's SQLite part waits for T029, but its file backend does not.
- T045 needs T040 (a principal for `state`) and T044.
- **Real connectors** (Salesforce, HubSpot, Gmail) are a later epic built on T044. The owner picks the first one.
