---
id: T095
title: "MCP OAuth for external servers: discovery, CIMD, DCR fallback, PKCE + resource, tokens in the vault"
epic: E19
status: done
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

- [x] **Discovery:** both the header and the well-known fallback are found; an RFC 8414 or OIDC issuer mismatch is refused.
- [x] **CIMD:** the authorization server fetches our metadata document, and the flow completes with the URL as `client_id`.
- [x] **DCR fallback:**
  - used only when neither a pre-registered client nor CIMD is supported;
  - the registered client is reused, not re-registered per user.
- [x] **PKCE + resource:** the fake authorization server rejects a flow without the S256 challenge, or with a wrong `resource`.
- [x] **State:** reused, expired or other-principal state is refused (fuzz the callback parameters).
- [x] **Refresh:** two concurrent calls with an expired token refresh once; `invalid_grant` gives "reconnect needed".
- [x] **Canary (E07):** a seeded token value appears in no log, event, error, API response or checkpoint.
- [x] **Cross-tenant:** tenant A's tokens or registered client never serve B.
- [x] **Mutation list** recorded.

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

### Phase 2 (2026-10-22): the vault, `serve`, and per-user toolsets

**What runs now.** An admin adds an MCP server at run time with `auth: oauth` and `scope: user` (or `tenant`); each person connects their own account through the agent API's connect flow; the agent calls the server with that person's token. Wired in `serve` when the agent component has a credential vault and `AGENTKIT_PUBLIC_URL` is set (`cmd/agentkit/toolsetadmin.go`): `oauth.New` over `oauth.VaultStore`, the runtime toolsets' SSRF guard as its HTTP client, `servers.WithOAuth`, the connect flow's `Dynamic` slots, and `/oauth/client-metadata.json` mounted beside `/oauth/callback` (served only on an https public URL, as CIMD needs).

Decisions:
- **The connect flow is T045's, not a second one.** `connect.Dynamic` (new) gives the flow slots that aren't in the config: one per OAuth toolset, named after it. State, the browser-binding cookie, single use, expiry and the return-URL allowlist are `connect.Service`'s. Only the authorization URL and the exchange come from `pkg/mcp/oauth`, split into `Start` (discovery and registration, a `Pending`), `AuthCodeURL` and `Finish`. The `Pending` (PKCE verifier, client secret) is sealed inside the flow blob (`EncodePending`). `Begin`/`Complete` with their own `StateStore` stay for other users of the package; `serve` passes no `States`.
- **Tokens are vault credentials.** A connection is an `OAuth2` credential at the toolset's slot for its owner, with a new `Client` field (client, token URL, issuer, resource) so a refresh needs nothing else, and `Extra`. A registered (DCR) client is a new `OAuth2Client` credential per tenant and issuer at a reserved slot, `_mcp-client-<sha256(issuer)[:20]>`; connect refuses slots starting with `_`, and dynamic slots never shadow config slots. The store's tenant is always the key's, never the context's.
- **Refresh is this package's singleflight plus the vault's compare-and-swap** (`Put` with the version read), not T044's `Sources`, which are built from the config's `credentials.oauth2` apps. `invalid_grant` is the vault's `MarkNeedsReconnect`, so `GET /v1/credentials` and `GET /v1/toolsets` say `needs_reconnect` as they do for T044's slots.
- **The authorization server is pinned when the toolset is added** (`Connector.Issuer`, kept as `issuer`). A pre-registered client (`oauth_client`, its secret a `${NAME}` from `header_env`) is sent only there. A token is used only for the server it was issued for: its URL or origin and its issuer (`issuedFor`). A toolset removed and added again under the same name elsewhere is not connected.
- **OAuth toolsets aren't probed when added.** Their tools are listed through the caller's connection, so the admin connects first and then reviews. A user who hasn't connected is offered the approved definitions and gets "not connected: connect your account to … on the Connections page" from a call, which never reaches the server. Whose connection, and that it is usable, is checked on every use; connections are cached per tenant, toolset and user. A refused refresh during a call is re-read from the store and said as "reconnect needed".
- **SSE is refused for OAuth**, and so are headers: the SDK's `OAuthHandler` is on the Streamable HTTP transport (`client.Config.OAuthHandler`).
- **`422 auth_required` says which sign-in** in a structured field (`error.auth`: `oauth` when the 401 names protected resource metadata, else `headers`), so the console no longer reads the message.
- **Console:** step 2 offers "Each person signs in" when `error.auth` is `oauth` and sends `auth: oauth`, `scope: user`. Step 3 asks the admin to **Connect** (a popup through `POST /v1/credentials/{name}/connect`, back at `/connections/done`, which reports on the existing channel) with **Check again**, then shows the review. Connections lists the toolset's slot like any other OAuth slot.

Criteria covered:
- Discovery: phase 1's tests, plus `TestDiscoveryOIDC` (OIDC only, and its issuer mismatch refused) and `TestIssuer`.
- CIMD, DCR fallback and reuse: phase 1's tests. DCR is now per tenant in the vault and shared by replicas (`TestVaultStore`). The DCR singleflight's key is checked deterministically: `TestDCRFlightPerTenant` holds one registration with `OnRegister`.
- PKCE + resource: phase 1's tests, and `TestStartFinish` (another verifier refused).
- State: phase 1's `TestState`, `FuzzComplete`; for the connect flow, `TestDynamic` (no cookie, another flow's cookie, expired, denied, no code, two codes, another state, used once, the toolset removed meanwhile, the flow sealed), `FuzzDynamicCallback`, `TestStartFinish` and `TestOAuthDynamicComplete` (another user's or toolset's flow).
- Refresh: `TestVaultStore` (8 concurrent calls make 1 refresh through the vault, a second replica agrees, `invalid_grant` marks `needs_reconnect`, connecting again clears it); `TestOAuthReconnect` (the tool error and the toolset's `connection`).
- Canary: phase 1's `TestCanary`; end to end, `cmd/agentkit` `TestMCPOAuthE2E`. That test runs serve's wiring, the API, the connect flow with a cookie jar, the fake authorization server and an SDK server, and agent runs as two users. It finds no access or refresh token in the logs (debug), any API response (run events included) or the agent store's SQLite files. The vault's sealing is `TestVaultStore`'s "sealed at rest".
- Cross-tenant: phase 1's `TestCrossTenant`; `TestVaultStore` (the key's tenant, never the context's; a DCR client per tenant); `TestOAuthUserScope` (the same user ID in another tenant is another owner); `TestDynamic` (another tenant's slot).
- T096's Scopes: `TestOAuthUserScope`, `TestOAuthTenantScope`, `TestMCPOAuthE2E`.
- Also: `TestIssuedFor`, `TestOAuthReAdded`, `TestOAuthAddErrors`; `TestServeRejectsBadSetup` (`AGENTKIT_PUBLIC_URL` must be an https origin, http only on loopback); the console's `mcp.test.tsx` (the OAuth add, connect and check-again path; per-user sign-in offered only on `auth: oauth`; `addError` reads the field, not the words) and `client.test.ts`; `make web-smoke` (28 passed).

**Live check.** No test account for a public MCP server here, so there is no `-tags live` run. The fake authorization server and the SDK server cover the flow.

**Mutation list, phase 2** (each planted, the named test run, then reverted; a build failure counted as no result):
- Caught (14):
  - the DCR flight keyed without the tenant (`TestDCRFlightPerTenant`);
  - `issuedFor` off (`TestIssuedFor`, `TestOAuthReAdded`);
  - the origin compared without its scheme (`TestIssuedFor`);
  - the vault store in the context's tenant (`TestVaultStore`);
  - Swap without its version (`TestVaultStore`);
  - `dynamic.Complete` without the owner check (`TestOAuthDynamicComplete`);
  - a connection used without checking it (`TestOAuthUserScope`);
  - an unconnected user's offer invoked (`TestOAuthUserScope`);
  - no re-check after a failed call (`TestOAuthReconnect`);
  - a dynamic slot shadowing a config slot (`TestDynamic`);
  - `_` slots not reserved (`TestDynamic`);
  - any scheme accepted for the authorization URL (`TestDynamic`);
  - a flow completed for a removed toolset (`TestDynamic`);
  - the 422 without `auth` (`TestMCPOAuthE2E`).
- Also caught (3):
  - an access token logged on connect (`TestCanary`, `TestMCPOAuthE2E`);
  - a literal client secret accepted (`TestOAuthAddErrors`);
  - scope `user` without OAuth (`TestOAuthAddErrors`).
- Not caught (1):
  - `dynamic.Complete` without its toolset check. `Finish` makes the same check (`ErrBadState`), so this is an equivalent mutant: the check is kept as defence in depth.
- Phase 1's miss (the DCR flight key) is now caught.

**Security review** (token leaks, open redirects, state binding):
- Tokens and client secrets are `Secret`s and sealed in the vault; tool errors and API responses carry statuses, never values (the canary above).
- **Fixed during the review:** the authorization URL comes from an authorization server's metadata and the console sends a same-origin popup to it, so connect now refuses one that isn't http(s) with a host (`javascript:` would have run as the console). The SDK's own check refuses only `javascript:`, `data:` and `vbscript:`.
- The return URL is the existing allowlist. The callback is T045's: cookie-bound, single use, sealed.
- `dynamic.Complete` checks the flow's owner and toolset against the callback's.
- A token goes only to the server and issuer it was issued for.
- Discovery and registration go through the SSRF guard.

**Size:** +241,664 bytes on the all-in-one binary, +249,856 on local and dev, +32,768 on api (docs/SIZES.md). dev is now 4,096 bytes under its budget.

### For T044 / E07

- `Sources` (T044) could take a vault-backed refresher per slot, so the config's `credentials.oauth2` apps and MCP OAuth toolsets share one refresh path. Today they share the vault's compare-and-swap and `NeedsReconnect`, not the code.
- `x/oauth2` can't send `resource` on a refresh (phase 1). A server that requires it on refresh would need our own refresh request.

### For E19 (later)

- MCP OAuth for **toolsets of the config** (`mcp.toolsets` with `type: mcp`): this task wires it for toolsets added at run time only. The config path would pass `OAuthHandler` from `pkg/mcp/client` with a slot from the config.
- A `-tags live` check against a public server once there is a test account.
- The server-side check of header values that look like keys (T096's open owner question) applies to `oauth_client.client_secret` too; it must already be a `${NAME}`.
