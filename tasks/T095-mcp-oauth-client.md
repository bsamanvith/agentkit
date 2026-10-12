---
id: T095
title: "MCP OAuth for external servers: discovery, CIMD, DCR fallback, PKCE + resource, tokens in the vault"
epic: E19
status: todo
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
