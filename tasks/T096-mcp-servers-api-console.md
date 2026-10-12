---
id: T096
title: "Add MCP servers at runtime: API and console (add by URL, probe, connect, review and pin tools, assign to agents), SSRF guard"
epic: E19
status: todo
depends_on: [T094, T062, T040, T082]
---

# T096: MCP servers API and console

## Context

The easy path the owner asked for: an admin pastes a URL, agentkit probes it, the admin reviews its tools, and users connect. No config edit, no restart. Decisions: [E19](E19-external-mcp-servers.md). The console screens are designed in [T087](T087-console-ux-design-2.md) (the "Add connector" screen T070 asked for). This task builds them on that design.

## Scope

- **API** (admin scope; in the OpenAPI spec):
  - `POST /v1/toolsets` with `{type: "mcp", name, url, auth: none|headers|oauth, scope: tenant|user, oauth_client?}`. It probes the server and returns:
    - the server's name, version and tool list with fingerprints;
    - whether auth is needed and of which kind;
    - the connect URL when OAuth is needed.
  - `GET /v1/toolsets/{name}/review` lists new, changed and removed tools, each with its raw description, its sanitized description and a diff.
  - `POST /v1/toolsets/{name}/review` approves or rejects tools and sets each tool's kind and risk (E17).
  - `PATCH` enables, disables or edits a toolset; `DELETE` removes it.
- Stored as data in the store (tenant-scoped), next to config-file toolsets, which stay read-only. Agents pick runtime toolsets the same way as config ones (T075 when agents are data).
- **Pinning enforced:**
  - approved fingerprints are stored per tool;
  - on connect or `list_changed`, a changed or new tool is disabled and the toolset shows "review needed";
  - a run sees only approved, unchanged tools.
- **SSRF guard:**
  - `https` only;
  - a dialer that checks the resolved IP at connect time and refuses loopback, private, link-local, CGNAT and cloud metadata addresses (IPv4 and IPv6, IPv4-mapped included);
  - redirects re-checked;
  - an operator allowlist for internal hosts.
- **Console** (on T087's design):
  - Toolsets: "Add MCP server" (URL → probe → auth → review tools → save), the review screen with diffs, and the "review needed" state.
  - Connections: each user's **Connect** for user-scoped servers, using T095's flow.
- **Agent API for harnesses:** everything above is in `/v1` and `agentkit` CLI subcommands (`agentkit toolsets add-mcp <url>`), so Claude Code can add and review servers too.

## Out of scope

- Registry search (T097).
- User-added private servers. The tenant setting exists and defaults to off; turning it on is a later task.

## Acceptance criteria

- [ ] **Add by URL:** against a fake MCP server, `POST /v1/toolsets` probes it and returns its tools; after review, an agent using the toolset calls a tool.
- [ ] **Rug pull:**
  - the fake server changes a tool's description;
  - the next connect disables that tool and the run no longer offers it;
  - the review shows the diff, and approval restores the tool.
- [ ] **New tool:** a tool the server adds later starts disabled.
- [ ] **SSRF:** URLs resolving to `127.0.0.1`, `10.x`, `169.254.169.254`, `::1`, `::ffff:127.0.0.1`, and a DNS name that rebinds to a private address between check and connect are all refused; an allowlisted internal host is accepted.
- [ ] **Scopes:**
  - a `user`-scoped server uses each user's own connection;
  - a user who hasn't connected gets "not connected" as a tool error, never another user's token.
- [ ] **Authorization:**
  - non-admins get 403 on add, review and delete;
  - a toolset outside the principal's `Toolsets` is invisible (E07).
- [ ] **Console:** the add → review → connect path passes the browser smoke test (`make web-smoke`).
- [ ] **Mutation list** recorded.

## References

- [Claude custom connectors](https://claude.com/docs/connectors/custom/remote-mcp) and [adding unlisted connectors](https://claude.com/docs/connectors/custom/add-unlisted) (the flow to match: URL → Add → Connect; org owner adds, members connect)
- [MCP security best practices](https://modelcontextprotocol.io/specification/2026-07-28/basic/security_best_practices) (SSRF, token passthrough, confused deputy)
- [OWASP SSRF prevention](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html)
- docs/UI.md "Connections" and "Toolset skipped"; T087
