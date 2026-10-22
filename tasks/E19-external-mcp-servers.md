---
id: E19
title: "External MCP servers: add any remote MCP server by URL, connect accounts with MCP OAuth, keep its tools safe"
status: todo
---

# E19: external MCP servers

Owner request (2026-10-12): "How can we add MCPs easily into the platform? How do other people do it? ... how do I enable my website to accept their MCP easily?"

Today agentkit **serves** MCP (E03: one server, one path per toolset) but cannot **use** anyone else's MCP server. PLAN.md has `mcp/client` ("connects the harness to external MCP servers ... wraps them as `tool.Tool`") but no task. This epic builds it, then makes adding a server as easy as it is in Claude or ChatGPT: paste a URL, click Connect, pick tools.

## How others do it (research, 2026-10-12)

| Product | How a server is added | Auth |
|---|---|---|
| **Claude** (claude.ai custom connectors) | Paste the remote MCP server's URL. Team/Enterprise: an owner adds it for the org; each member clicks **Connect** to sign in. | MCP OAuth (discovery + registration + PKCE); optional "advanced" OAuth client ID/secret for servers that need a pre-registered client. Remote only: local stdio servers (`npx`, `uvx`) can't be connectors. |
| **ChatGPT** (connectors / apps, developer mode) | Paste the URL. | Same MCP OAuth, or none. |
| **Claude Code, Cursor, VS Code** | `claude mcp add --transport http <name> <url> --header ...`, or `mcp.json`. Also run local stdio servers. | MCP OAuth (`/mcp` starts it) or static headers. |
| **Directories** | The official **MCP Registry** (`registry.modelcontextprotocol.io`, preview, API v0.1, about 17k servers). It holds only `server.json` metadata pointing at packages or remote URLs, with "subregistries" adding ratings and scans. Smithery, Glama, Docker's catalog and others curate on top. | n/a |

What the auth flow is (MCP authorization spec, revision 2026-07-28):
1. **Discovery.** The client calls the server and gets **401** with `WWW-Authenticate: Bearer resource_metadata=...`. It reads the server's **protected resource metadata** (RFC 9728) to find its authorization server, then that server's metadata (RFC 8414 or OIDC discovery).
2. **Client registration**, in this order of preference:
   - a pre-registered client ID and secret;
   - **Client ID Metadata Documents (CIMD)**: the client's `client_id` is an HTTPS URL it hosts, and the authorization server fetches the client's metadata from it. This is now the preferred way;
   - Dynamic Client Registration (RFC 7591), kept only for backward compatibility. A 2026 study found DCR flaws on most of the remote servers it tested.
3. **OAuth 2.1** authorization code + **PKCE**, with the `resource` parameter (RFC 8707) naming the MCP server. Tokens are refreshed like any OAuth2 token.

Known problems to learn from:
- **Tool poisoning.** Instructions are hidden in a tool's description, and the model reads them as trusted.
- **Rug pulls.** A server changes its tool descriptions after it was approved.
- **Cross-server shadowing.** One server's tool names or descriptions steer the model toward another server's tools.
- **SSRF.** A user-supplied URL points at internal services.
- **Token passthrough.** The common defenses: pin a hash of each tool's definition when it is approved and block on change, show the raw description when approving, sanitize descriptions, and treat every result as untrusted.

## Decisions (made in planning; change them in the task that finds them wrong)

| Question | Decision |
|---|---|
| What a server becomes | A **toolset** of type `mcp` (E03's toolsets, `pkg/toolset`). Its tools are `tool.Tool`s, so agents, E17 policies, the console's Toolsets page and our own MCP server treat them like built-in connectors. Tool names are prefixed with the toolset name (`linear__create_issue`) so two servers can't shadow each other. |
| Transports | **Streamable HTTP**, plus the older HTTP+SSE transport as a fallback. **stdio** only from the operator's config file (a command the operator chose), never from the API or the console. A web user can never make the server run a command. |
| Who adds a server | **Admins**, as a tenant toolset (the Claude Team model): API or console, no restart. Each user then **connects their own account** on the Connections page (user scope), or the admin connects one shared account (tenant scope). Users adding their own private servers is a tenant setting, **off** by default. |
| Auth kinds | None; static headers whose values come from a credential slot (an API key); **MCP OAuth**. MCP OAuth tries a pre-registered client first, then **CIMD**, with agentkit hosting its client metadata document at a stable HTTPS URL. DCR is the last fallback and stores the registered client per tenant. PKCE (S256) and the `resource` parameter are always sent. Tokens live in the E07 vault, sealed, under the server's slot, and refresh through T044's singleflight + compare-and-swap. |
| No token passthrough | The tokens an agentkit caller sends are never forwarded to an external MCP server, and an external server's tokens never leave the vault except to that server (E07's rule). |
| Pinning (rug pulls) | When an admin approves a server's tools, each tool's name, description, input schema and annotations are **hashed and stored**. On connect and on `notifications/tools/list_changed`, a tool whose hash changed is **disabled** until an admin reviews the diff. A new tool starts disabled. Removed tools simply disappear. |
| Risk class | External tools default to E17's unclassified class (`write`, `medium`). MCP annotations (`readOnlyHint`, `destructiveHint`) are **claims**: they may raise a tool's risk on their own, but lowering it to `read` takes an admin's confirmation in the review. |
| Untrusted output | Tool results from external servers are third-party data (E10's prompt-injection rule). Results have a size cap. Descriptions are sanitized: control characters and invisible Unicode are stripped, the length is capped, and the raw text is shown to the admin during review. |
| SSRF | A URL added through the API must be `https`. The dialer refuses loopback, private, link-local and metadata addresses **at connect time**, not only when the URL is parsed, so DNS rebinding doesn't get around it. The operator can allowlist internal hosts in config. Redirects are followed only to hosts that pass the same check. |
| Registry | Later (T097): search the official MCP Registry for servers with a `remotes` entry and prefill the add form. A tenant can restrict additions to an allowlist. The registry is a search aid, not a trust signal. |
| Library | The official Go SDK's client (`modelcontextprotocol/go-sdk`, already our server's SDK): `mcp.Client` and `StreamableClientTransport`. Its OAuth client support is new (`auth` package, `OAuthHandler`), so T095 checks whether it covers CIMD, the `resource` parameter and our vault. If not, T095 drives the flow itself with `golang.org/x/oauth2` and the SDK's `oauthex` metadata types. |

## Tasks

- [x] [T094](T094-mcp-client-toolset.md) `pkg/mcp/client` and the `mcp` toolset type: connect, list, call, pin, annotations, from config
- [x] [T095](T095-mcp-oauth-client.md) MCP OAuth for external servers: discovery, CIMD, DCR fallback, PKCE + resource, tokens in the vault
- [x] [T096](T096-mcp-servers-api-console.md) Add servers at runtime: API and console (add by URL, probe, connect, review tools, assign to agents), SSRF guard
- [x] [T097](T097-mcp-registry-search.md) Search the MCP Registry from the add form (later)

Order:
- **T094 can start now.** It needs only `pkg/tool` and `pkg/toolset`, and it tests against our own MCP server. Static-header auth reads env vars until T044 lands.
- T095 needs T042 and T044 (vault and slots) and shares T045's callback and `state` handling. Whichever of T045 and T095 starts second reuses the first one's flow.
- T096 needs T094, T062 (catalog API), T040 (principals) and T082 (tool metadata). Its console screens are designed in T087 part 2 (the "Add connector" screen T070 asked for).
