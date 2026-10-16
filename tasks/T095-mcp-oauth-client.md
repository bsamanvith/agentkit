---
id: T095
title: "MCP OAuth for external servers: discovery, CIMD, DCR fallback, PKCE + resource, tokens in the vault"
epic: E19
status: in-progress
depends_on: [T094, T042, T044]
---

# T095: MCP OAuth client

## Context

Most hosted MCP servers (Linear, Notion, GitHub, Atlassian, ...) sign users in with MCP OAuth. A user should be able to click **Connect** on a server and sign in, the way they would in Claude. Decisions: [E19](E19-external-mcp-servers.md) ("Auth kinds", "No token passthrough"), [E07](E07-auth-credentials.md) (vault, slots, refresh).

## Scope

- **Discovery:**
  - on 401, read `WWW-Authenticate` `resource_metadata`, or fall back to `/.well-known/oauth-protected-resource` on the server's origin and path (RFC 9728);
  - then the authorization server's metadata (RFC 8414, falling back to OIDC discovery);
  - cache it with a TTL.
- **Client registration**, tried in order:
  1. a pre-registered client ID and secret from the toolset config or the add form;
  2. **CIMD**: agentkit serves its client metadata document at `GET /oauth/client-metadata.json` on its public base URL (`AGENTKIT_PUBLIC_URL`), and that URL is the `client_id`;
  3. DCR (RFC 7591), sending `application_type: web`; the registered client is stored per (tenant, server) in the vault.
- **Flow:**
  - authorization code + PKCE S256, with `resource` (RFC 8707) set to the server's canonical URL;
  - the scopes the server's metadata or 401 asks for;
  - `state` is single-use, expires and is bound to the principal. Share T045's `/oauth/callback` and state store; whichever of T045 and T095 lands second reuses the first one's.
- **Tokens:**
  - stored in the vault under the toolset's slot, `user` or `tenant` scope per the toolset;
  - refreshed through T044's singleflight + CAS;
  - `invalid_grant` marks the connection "reconnect needed".
  - A 401 or `insufficient_scope` during a call becomes a tool error ("not connected" or "needs more access"), and the console offers Connect.
- **Go SDK first:** use `auth` and `oauthex` from the SDK where they fit (the `OAuthHandler` on `StreamableClientTransport`). Record in Notes what was used and what was written ourselves.

## Out of scope

- The console screens (T096, designed in T087).
- Acting as an authorization server: we never issue tokens.

## Acceptance criteria

All criteria run against a **fake authorization server and fake MCP server** in tests, with a separate live check (`-tags live`) noted for one public server if a test account exists.

- [ ] **Discovery:** both the header and the well-known fallback are found; an RFC 8414 or OIDC issuer mismatch is refused.
- [ ] **CIMD:** the authorization server fetches our metadata document, and the flow completes with the URL as `client_id`.
- [ ] **DCR fallback:**
  - used only when neither a pre-registered client nor CIMD is supported;
  - the registered client is reused, not re-registered per user.
- [ ] **PKCE + resource:** the fake authorization server rejects a flow without the S256 challenge, or with a wrong `resource`.
- [ ] **State:** reused, expired or other-principal state is refused (fuzz the callback parameters).
- [ ] **Refresh:** two concurrent calls with an expired token refresh once; `invalid_grant` gives "reconnect needed".
- [ ] **Canary (E07):** a seeded token value appears in no log, event, error, API response or checkpoint.
- [ ] **Cross-tenant:** tenant A's tokens or registered client never serve B.
- [ ] **Mutation list** recorded.

## References

- MCP authorization (2026-07-28): [client registration](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/client-registration) (CIMD preferred, DCR for backward compatibility)
- RFC 9728 (protected resource metadata), RFC 8414 (authorization server metadata), RFC 7591 (DCR), RFC 8707 (resource indicators), RFC 7636 (PKCE)
- [WorkOS on CIMD vs DCR](https://workos.com/blog/mcp-client-identity-dcr-cimd-auth-md); [Claude custom connectors](https://claude.com/docs/connectors/custom/remote-mcp) (the user flow to match)
- [Go SDK `auth` package](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/auth)
- T044 (refresh, slots), T045 (callback, state)

## Notes

### Phase 1 (2026-10-15): the core, `pkg/mcp/oauth`

Owner request (2026-10-15): "add oauth support for things while users are connecting to mcp. Cause salesforce or future email all needs this", and "we need common utilities. But for each mcp/connector, it varied a bit". Built ahead of T094, T042 and T044, so it brings what it needs from them in small form: a token store interface with an in-memory implementation (the vault backs it later), and its own singleflight plus compare-and-swap refresh.

**Shared core, per-provider `Profile`.** Everything the spec fixes is in the core. What providers vary on is data in `oauth.Profile`: a pre-registered client, fixed endpoints (no discovery), extra auth params, scopes and the refresh scope's name, `OmitResource`, a default token lifetime, token fields to keep, plus one code hook (`OnToken`). Presets in `oauth.Profiles()`:
- `mcp`: the spec.
- `salesforce`: `refresh_token` scope, no `expires_in` (1 h default), keeps `instance_url`; `SalesforceEndpoints` for the login host or My Domain.
- `google`: fixed endpoints, `access_type=offline` + `prompt=consent`, no refresh scope.
- `microsoft`: `offline_access`, no `resource` (v2 refuses it with scopes); `MicrosoftEndpoints` for the tenant.

**Flow split for a web server.** `Begin` returns the authorization URL; `Complete` takes the callback's code, state and iss. The callback page is the console's (T096): it posts them to the API with the user's own credential, which is how the state is checked against the principal. `Handler` serves the CIMD document at `/oauth/client-metadata.json`.

**Go SDK.** Used: `oauthex` (protected resource and authorization server metadata with their resource, issuer and PKCE checks, `ParseWWWAuthenticate`, `RegisterClient`), `auth.GetAuthServerMetadata` (8414 then OIDC URLs), and `auth.OAuthHandler` as the interface `Connector.OAuthHandler` implements for `StreamableClientTransport`. Not used: `auth.AuthorizationCodeHandler`. It blocks in one call while a fetcher waits for the browser and keeps one token per handler, and it tries CIMD before a pre-registered client. Written here: the order of registration (pre-registered, CIMD, DCR per tenant and issuer), the state store, per-owner tokens, refresh, and 401/403 mapped to `ErrNotConnected`, `ErrReconnectNeeded` and `ErrNeedsMoreAccess`. Refresh goes through `x/oauth2`, which cannot add `resource` to a refresh request (the SDK has the same gap).

**Criteria covered** (`pkg/mcp/oauth/oauth_test.go`, against `oauthtest.AuthServer` and `oauthtest.Protect` around a real SDK server):
- Discovery: header and well-known (`TestConnectAndCall`, `TestDiscovery`); issuer mismatch and no PKCE refused.
- CIMD, DCR fallback and reuse, pre-registered first: `TestRegistration`.
- PKCE and resource: `TestPKCEAndResource`.
- State: `TestState`, `TestIssuerParameter`, `FuzzComplete`.
- Refresh: `TestRefresh` (8 concurrent callers make 1 refresh, two replicas on one store agree, `invalid_grant` gives reconnect, a token the server drops is refreshed and the call retried).
- Canary: `TestCanary`.
- Cross-tenant: `TestCrossTenant`.

**Mutation list** (each planted, then reverted):
- Caught: owner binding of state; state expiry; resource on the authorization request; DCR client stored per tenant; `invalid_grant` handling; forced refresh on 401; singleflight; iss check; `Secret` redaction; `insufficient_scope`.
- Missed: the DCR singleflight's key without the tenant. That bug would need two tenants registering at the same moment; there's no deterministic test yet.

**Left for T095:**
- Store tokens and registered clients in the vault (T042) instead of memory.
- Wire into `serve`: `AGENTKIT_PUBLIC_URL`, mount `Handler`, and the connect/complete API, with T096.
- Use `OAuthHandler` from T094's client.
- A `-tags live` check against one public server.
- A deterministic test for the DCR singleflight.

