---
id: T041
title: JWT/OIDC authenticator and MCP protected resource metadata
epic: E07
status: todo
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

## Acceptance criteria

- [ ] **Valid token:** a token signed by a key in the JWKS (test server via `httptest`, keys generated in the test) gives the mapped principal.
- [ ] **Rejected tokens** (each a case, with 401 and `error="invalid_token"`):
  - a bad signature;
  - `alg: none`;
  - HS256 signed with the public key's bytes;
  - wrong `iss`;
  - `aud` not including ours;
  - expired beyond the leeway;
  - `nbf` in the future beyond the leeway;
  - missing `exp`;
  - an unknown `kid` after a refresh;
  - a missing tenant claim;
  - an invalid tenant ID.
- [ ] **Key rotation:** the provider adds a new key and signs with it; the first token with the new `kid` triggers one refresh and succeeds. A burst of 100 unknown `kid`s within the minimum interval causes at most one fetch.
- [ ] **JWKS endpoint down:** previously cached keys keep working, and a token needing a new key fails with 401, not 500.
- [ ] **Leeway boundaries** are tested on both sides with the fake clock.
- [ ] **Protected resource metadata:** `/.well-known/oauth-protected-resource` returns the configured resource, authorization servers and scopes, and a 401 from `/mcp/x` carries `resource_metadata` pointing at it.
- [ ] **Shared suite:** passes `authtest.RunAuthenticator`.
- [ ] **Fuzzing:** `FuzzJWT` (malformed segments, huge headers, nested JSON) never panics and never authenticates.
- [ ] **Mutation list** recorded (at least: audience not checked, alg allowlist ignored, leeway applied in the wrong direction, refresh on every unknown `kid`).
