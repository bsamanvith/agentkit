---
id: T041
title: JWT/OIDC authenticator and MCP protected resource metadata
epic: E07
status: done
depends_on: [T039]
---

# T041: JWT/OIDC authenticator

## Context

API keys suit services. People and MCP clients that support OAuth (Claude Code) need tokens from an identity provider. We are only a resource server: we verify tokens; we never issue them. Decisions: [E07](E07-auth-credentials.md).

## Scope

- **`auth/jwt`** (or `auth.JWT`): an `auth.Authenticator` configured with:
  - `issuer`, `audience` (our resource URL), `jwks_url` (or discovered from `{issuer}/.well-known/openid-configuration`);
  - allowed algorithms (default `RS256`, `ES256`; never `none` or HMAC);
  - leeway (default 60 s);
  - claim mapping: tenant claim (default `tenant`, or a fixed tenant), user claim (default `sub`), scopes claim (`scope` space-separated, or `scp` array), toolsets claim or a scope prefix (`mcp:<toolset>`).
- **Checks:** signature, `iss` exact, `aud` contains our audience (MCP spec: tokens must be issued for this resource), `exp` required, `nbf`/`iat` with leeway, `kid` present when the set has more than one key.
- **JWKS cache:**
  - refreshed on an unknown `kid`, at most once per minimum interval (default 1 min), so a flood of fake `kid`s cannot hammer the provider;
  - honors cache headers within bounds;
  - keeps serving the last good set when a refresh fails;
  - uses the injected clock (`pkg/clock`).
- **MCP discovery:**
  - a handler for `/.well-known/oauth-protected-resource` (and the path-suffixed form for `/mcp/{name}`), built on the SDK's `auth.ProtectedResourceMetadataHandler` and `oauthex.ProtectedResourceMetadata`;
  - the middleware's `resource_metadata` option is set in the 401 header;
  - wired in `agentkit serve` when JWT auth is configured, beside API keys through `auth.Chain`.
- **Library:** go-jose v4 (planner decision; see E07 for the allowed alternative). Add it to the archtest allowlist for this package only.

## Out of scope

- Issuing tokens, an authorization server, dynamic client registration. Token introspection for opaque tokens (later, if an identity provider needs it).

## References

- [MCP authorization spec](https://modelcontextprotocol.io/specification/latest/basic/authorization) (resource server role, audience binding, no token passthrough)
- [RFC 9728: OAuth 2.0 Protected Resource Metadata](https://www.rfc-editor.org/rfc/rfc9728)
- [RFC 8725: JWT Best Current Practices](https://www.rfc-editor.org/rfc/rfc8725) (algorithm allowlists, `none`, key confusion)
- [RFC 7517: JWK](https://www.rfc-editor.org/rfc/rfc7517); [go-jose v4](https://github.com/go-jose/go-jose)
- MCP Go SDK `auth` and `oauthex` packages (`ProtectedResourceMetadataHandler`)

## Acceptance criteria

- [x] **Valid token:** a token signed by a key in the JWKS (test server via `httptest`, keys generated in the test) gives the mapped principal. — `TestValidTokenGivesMappedPrincipal` (13 rows: default claims, `scp` arrays, `aud` arrays, scope and scp together, patterns dropped, a fixed tenant, every claim name configured, no `sub`, `aud` naming the MCP endpoint asked for (RFC 8707) or a prefix of it, no `nbf`/`iat`, RS256, EdDSA when allowed). The provider is `jwttest.Issuer`, an `httptest` server with keys made in the test.
- [x] **Rejected tokens** (each a case, with 401 and `error="invalid_token"`): — `TestRejectedTokens` (39 cases, each through `auth.Middleware`, checking 401 and `error="invalid_token"`, and through `Authenticate`, checking `ErrInvalidCredentials`; then the same claims with our key pass, so each case fails for its own reason):
  - a bad signature; — another key under our kid, and a signature swapped from another token
  - `alg: none`; — `none`, `none` with a signature, `None`
  - HS256 signed with the public key's bytes; — with the JWK JSON, with the PEM, with the PEM and no kid
  - wrong `iss`; — also an issuer with a trailing slash, no `iss`
  - `aud` not including ours; — also our URL as a host or port prefix (with and without our path), another MCP endpoint, a list without ours, no `aud`, a number
  - expired beyond the leeway;
  - `nbf` in the future beyond the leeway; — and `iat`
  - missing `exp`; — and `exp` as a string, `exp` of 1e300
  - an unknown `kid` after a refresh;
  - a missing tenant claim;
  - an invalid tenant ID. — `../globex`, empty, an array
  - Also: no kid with several keys in the set, a kid of a key of another type, an algorithm not allowed (EdDSA), an algorithm that is not the key's own `alg` (PS256 with an RS256 key), an RSA key under 2048 bits, a user with a control character, a scope that is a number, claims that are not an object, claims with trailing data.
- [x] **Key rotation:** the provider adds a new key and signs with it; the first token with the new `kid` triggers one refresh and succeeds. A burst of 100 unknown `kid`s within the minimum interval causes at most one fetch. — `TestKeyRotation`: known keys fetch nothing; the new kid makes exactly one fetch and succeeds; 100 concurrent unknown kids right after a fetch make none, after the interval exactly one, and 1 s short of the next interval none; a key taken out of the set fails once the set is fetched again.
- [x] **JWKS endpoint down:** previously cached keys keep working, and a token needing a new key fails with 401, not 500. — `TestJWKSEndpointDown`: three rounds 2 h apart (the set stale, every fetch failing): cached key 200, new key 401 `invalid_token`; the failure is logged at warn; `Refresh` reports it; back up, the new key works; down from the start, 401.
- [x] **Leeway boundaries** are tested on both sides with the fake clock. — `TestLeewayBoundaries`: `exp` at now−leeway+1s (accepted) and now−leeway (refused), `nbf` and `iat` at now+leeway (accepted) and +1s (refused); with the clock moved instead of the claims, 1 ns before `exp`+leeway and at it; a fractional `exp` to the millisecond; a configured 5 s leeway.
- [x] **Protected resource metadata:** `/.well-known/oauth-protected-resource` returns the configured resource, authorization servers and scopes, and a 401 from `/mcp/x` carries `resource_metadata` pointing at it. — `pkg/auth/resourcemeta`: `TestMetadata` (fetched with the SDK's `oauthex.GetProtectedResourceMetadata`, which checks the resource per RFC 9728 §3.3; other paths 404), `TestUnauthorizedPointsAtMetadata` (the 401 parsed with `oauthex.ParseWWWAuthenticate`, then followed). In the binary, `TestE2EServeJWT` (`cmd/agentkit`): the server's metadata, and a 401 from `/mcp/demo-a`, with and without a bad token, pointing at `/.well-known/oauth-protected-resource/mcp/demo-a`, whose resource is the endpoint.
- [x] **Shared suite:** passes `authtest.RunAuthenticator`. — `TestConformance` (two tenants: a user token and a service token).
- [x] **Fuzzing:** `FuzzJWT` (malformed segments, huge headers, nested JSON) never panics and never authenticates. — each input is used whole and as header, claims and signature encoded here, against an authenticator that knows a real key (so a header naming it reaches the signature check); an internal error also fails. `go test -fuzz FuzzJWT -fuzztime 30s`: 133,370 runs, clean.
- [x] **Mutation list** recorded (at least: audience not checked, alg allowlist ignored, leeway applied in the wrong direction, refresh on every unknown `kid`). — below: 36 plants, 34 caught, 2 equivalent.

## Notes

### Files

- **`pkg/auth/jwt`** (new): `doc.go`, `jwt.go` (`Config`, `New`, `Authenticator`, the claim checks and mapping), `keys.go` (the JWKS cache, discovery, cache headers). Tests: `helpers_test.go` (the rig), `jwt_test.go`, `fuzz_test.go`, `example_test.go`.
- **`pkg/auth/jwt/jwttest`** (new): `Issuer`, a fake identity provider (OpenID configuration, JWKS, keys made in the test, rotation, down, cache headers, a fetch count) and `Compact` for tokens no key makes.
- **`pkg/auth/resourcemeta`** (new): `Handler` (RFC 9728 on the SDK's `auth.ProtectedResourceMetadataHandler` and `oauthex.ProtectedResourceMetadata`), `URL`, `Middleware` (auth's, with a per-endpoint `resource_metadata`). Its own package so that only what serves MCP links the SDK: `agentkit-api` and `-runner` (`nomcp`) must not (`scripts/check-deps.sh`).
- **`cmd/agentkit`**: `auth.go` (`auth.jwt`, `Chain(jwt, apiKeys)`, a fetch at startup that only warns), `mcpserver.go` (the per-endpoint 401 and the two metadata mounts, `/.well-known/oauth-protected-resource` and `.../`), `mcpserver_none.go`, `mcpconfig.go` (an unexported field serve sets), `serve.go`; tests `jwt_e2e_test.go` (the binary with a fake provider) and five config rows in `serve_test.go`.
- **Shared:** `go.mod`/`go.sum` (go-jose v4.1.6), `internal/archtest/rules.go` (go-jose for `pkg/auth/jwt` and `jwttest` only; the SDK's `auth` and `oauthex` for `resourcemeta`; their module imports), `Makefile` (local budget 27 → 28 MB, toolsets 17 → 18 MB, see Sizes), `docs/SIZES.md`, `docs/QUICKSTART.md` (the `auth.jwt` section).

### Configuration (`auth.jwt`)

```json
{"auth": {"jwt": {
  "issuer": "https://id.example.com/", "audience": "https://agents.example.com",
  "jwks_url": "", "algorithms": ["RS256", "ES256"], "leeway": "60s",
  "tenant": "", "tenant_claim": "tenant", "user_claim": "sub", "scopes_claim": "",
  "toolsets_claim": "", "toolset_scope_prefix": "mcp:",
  "min_refresh_interval": "1m", "max_cache_age": "1h",
  "authorization_servers": [], "scopes_supported": []
}}}
```

### Decisions (made here; change them in the task that finds them wrong)

- **go-jose, used narrowly.** `jose.ParseSignedCompact` with the configured algorithms, then `Verify` with the one key the kid names; the claims are decoded and checked here (not with go-jose's `jwt` package), so every check is ours and visible to the mutation list. JWKs are read one by one, so a key go-jose cannot read (a new type) is skipped rather than failing the set. Size: see below; golang-jwt was not tried, as it has no JWK reader and would need one written.
- **Algorithms:** RS256 and ES256 by default; a config may allow RS/PS 256-512, ES256-512 and EdDSA. `none` and every HMAC are refused at construction, and go-jose refuses any algorithm not in the list at parse. A key is used only for an algorithm of its type and curve, and of its own `alg` if the JWKS gives one. RSA keys under 2048 bits, symmetric keys and `use: enc` keys are dropped when the set is read.
- **Audience:** `aud` must contain the configured audience exactly, or a URL under it (with a `/` boundary) whose path holds the request's path. That accepts what an MCP client asks for under RFC 8707 (the endpoint's own URL, `https://host/mcp/crm`), but not that token on `/mcp/gmail` or `/v1`.
- **Times:** `exp` required; valid while now < exp + leeway. `nbf` (if present) fails when now < nbf − leeway; `iat` (if present) fails when it is after now + leeway. Leeway 0 to 5 min, default 60 s. Fractional dates are kept to the nanosecond; dates beyond ±2⁴⁰ s are refused.
- **The key cache:** one fetch at a time; a fetch is skipped when one *started* less than `min_refresh_interval` ago (failures count, so a provider that is down is not hammered either). The set goes stale after its `Cache-Control: max-age` (or `Expires` − `Date`), clamped to [interval, `max_cache_age`]; `no-store`/`no-cache` mean the interval; no header means `max_cache_age`. A stale set is refetched (rate-limited) before use; a failed fetch keeps the last good set, however old. An empty or unreadable set is a failed fetch. Fetches use the request's context without its cancellation, plus a 10 s timeout, and read at most 1 MiB. URLs must be https, or http on a loopback host (the issuer, the JWKS URL and a discovered `jwks_uri`); nothing in a token (`jku`, `x5u`, `jwk`) is ever fetched or used.
- **Discovery:** with no `jwks_url`, `{issuer}/.well-known/openid-configuration`, whose `issuer` must be ours exactly. Found once, then kept. RFC 8414's `oauth-authorization-server` document is not tried.
- **Not a JWT is no credentials:** a bearer token that is not two dots and base64url characters (first segment non-empty) gives `ErrNoCredentials`, so `Chain(jwt, apiKeys)` passes API keys on (T039's "For T041"). An empty third segment (`alg: none`) is still JWT-shaped, so it is refused as invalid, not passed on.
- **Claims to a principal:** tenant from `tenant_claim`, or `tenant` fixed for all (then no claim is read); user from `sub` (absent: no user; it must be ≤ 256 bytes of UTF-8 without control characters, like an API key's); scopes from `scope` and `scp` (strings split on spaces, or arrays), or one configured claim; toolsets from scopes `mcp:<toolset>` and an optional toolsets claim. Duplicates once. **Values with `*`, `?`, `[` or `\` are dropped**: `Principal.Scopes` and `Toolsets` are `path.Match` patterns, and a provider that lets clients ask for any scope string must not be able to grant `*` or `mcp:*`. A deployment that wants "all toolsets" for a token lists them in the toolsets claim. `Via` is `{Kind: "jwt", ID: <issuer>}`.
- **Errors** name the failed check and never a value from the token. The key cache logs failed fetches at warn (issuer and error, no token).
- **Protected resource metadata** is served only with `auth.jwt` and the MCP server: at `/.well-known/oauth-protected-resource` (resource: the audience) and at that path plus an endpoint's path (resource: the endpoint's URL, which is what the SDK's client checks, RFC 9728 §3.3). Other suffixes 404. `authorization_servers` defaults to the issuer; `bearer_methods_supported` is `["header"]`. With it, the audience must be the server's origin, or startup fails. A 401 on `/mcp/<name>/...` points at that endpoint's metadata; on `/mcp/` at the root's. `/v1` 401s carry no `resource_metadata` (no client discovers the agent API that way; `pkg/agentapi` builds its own middleware and would need an option).
- **Startup:** serve fetches the keys once and only warns on failure; the authenticator retries on the next token (after the interval).

### Sizes

go-jose is new. Stripped `linux/amd64` binaries, main at `617c683` and the branch: agentkit +299,008, local +450,560, dev +303,104, api +282,624, runner +442,368, toolsets +454,656. Local projects to about 27.08 MB and toolsets to about 17.11 MB, over their 27 and 17 MB budgets, so they are now 28 and 18 MB (rows in `docs/SIZES.md`). No Docker here, so no image was measured. What gets cut first: verifying with the standard library and a small JWK reader in place of go-jose; and the runner, which authenticates nobody, links it only through serve's wiring.

### Mutation check

Planted one at a time (`t041/mutate.py` in the session scratchpad; `go test -race` on `pkg/auth/jwt/...` and `pkg/auth/resourcemeta/...`, or `TestE2EServeJWT` for `cmd/agentkit`), then restored and diffed against a backup.

| Planted bug | Caught by |
|---|---|
| **Audience not checked** | `TestRejectedTokens` |
| Audience: any URL under ours, whatever the request's path | `TestRejectedTokens` |
| Audience: a host prefix (no `/` boundary) | `TestRejectedTokens` (added after it first survived: our URL as a host prefix *with our path*) |
| **Algorithm allowlist ignored at parse** (every algorithm, HS256 and none included) | `TestRejectedTokens` |
| HS256 accepted in the configuration | `TestConfigErrors`, `Example` |
| **Leeway in the wrong direction**: `exp` / `nbf` / `iat` | `TestLeewayBoundaries` / nearly every test / nearly every test |
| `exp` boundary off by one (`After` for `!Before`) | `TestLeewayBoundaries` |
| `exp` not required | `TestRejectedTokens` |
| `nbf` not checked / `iat` not checked | `TestLeewayBoundaries`, `TestRejectedTokens` |
| Issuer compared as a prefix | `TestRejectedTokens` |
| A missing tenant claim gives a default tenant | `TestRejectedTokens` |
| The tenant id cleaned instead of refused | `TestRejectedTokens` |
| Patterns in claims kept | `TestValidTokenGivesMappedPrincipal` |
| A non-JWT is invalid (the chain stops before the API keys) | `TestNotAJWTIsNoCredentials`, `Example` |
| An empty signature counts as not a JWT (`alg: none` passes on) | `TestRejectedTokens` |
| **Refresh on every unknown kid** | `TestKeyRotation`, `TestJWKSEndpointDown`, `TestDiscovery` |
| The rate limit counts only successful fetches | `TestKeyRotation`, `TestJWKSEndpointDown`, `TestDiscovery` |
| No single-flight (no fetch lock) | `TestConformance` (`ConcurrentCalls`: the first calls find a fetch already started, skip it, and see no keys yet) |
| The kid not required when the set has several keys | `TestRejectedTokens` |
| The key's own `alg` ignored | `TestRejectedTokens` |
| RSA keys under 2048 bits used | `TestRejectedTokens` (added after it first survived) |
| A failed fetch drops the last good set | `TestJWKSEndpointDown` |
| A stale set never refetched | `TestCacheHeaders`, `TestKeyRotation` |
| Cache headers ignored / `no-store` ignored | `TestCacheHeaders` |
| The discovered configuration's issuer not checked | `TestDiscovery` |
| A 401's metadata always the root's | `TestUnauthorizedPointsAtMetadata` |
| An endpoint's metadata names the root as its resource | `TestMetadata`, `TestUnauthorizedPointsAtMetadata`, `Example` |
| Metadata served for any path | `TestMetadata` |
| serve: MCP 401 without `resource_metadata` / no metadata mounted | `TestE2EServeJWT` |
| *Key type not matched to the algorithm* | **equivalent**: go-jose's `Verify` refuses a key of another type, so the token still fails (`kid of a key of another type` in `TestRejectedTokens`). The check stays as defense in depth. |
| *`max-age` not bounded below by the interval* | **equivalent**: a stale set is refetched only through the rate limit, which already holds fetches to one per interval. |

### For T061 (local users and login)

- The chain becomes `Chain(sessions, jwt, apiKeys)`. The JWT authenticator reads only `Authorization`, so a cookie request with no header passes through it as `ErrNoCredentials`.

### For later

- `/v1` 401s could carry `resource_metadata` too (an `agentapi` option passing `auth.WithResourceMetadata`), if an OAuth client ever discovers the agent API.
- Token introspection for opaque tokens, and RFC 8414 discovery, if a provider needs them (out of scope here).
- If the image budgets bite, replace go-jose with the standard library's verifiers and a JWK reader (the checks and the tests stay).
