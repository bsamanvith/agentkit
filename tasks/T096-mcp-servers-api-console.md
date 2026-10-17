---
id: T096
title: "Add MCP servers at runtime: API and console (add by URL, probe, connect, review and pin tools, assign to agents), SSRF guard"
epic: E19
status: in-progress
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

- [x] **Add by URL:** against a fake MCP server, `POST /v1/toolsets` probes it and returns its tools; after review, an agent using the toolset calls a tool.
- [x] **Rug pull:**
  - the fake server changes a tool's description;
  - the next connect disables that tool and the run no longer offers it;
  - the review shows the diff, and approval restores the tool.
- [x] **New tool:** a tool the server adds later starts disabled.
- [x] **SSRF:** URLs resolving to `127.0.0.1`, `10.x`, `169.254.169.254`, `::1`, `::ffff:127.0.0.1`, and a DNS name that rebinds to a private address between check and connect are all refused; an allowlisted internal host is accepted.
- [ ] **Scopes** (needs T095 and T044; part 2 refuses `scope: user`):
  - a `user`-scoped server uses each user's own connection;
  - a user who hasn't connected gets "not connected" as a tool error, never another user's token.
- [x] **Authorization:**
  - non-admins get 403 on add, review and delete;
  - a toolset outside the principal's `Toolsets` is invisible (E07).
- [ ] **Console:** the add → review → connect path passes the browser smoke test (`make web-smoke`).
- [ ] **Mutation list** recorded (parts 1 and 2 below; part 3 to come).

## References

- [Claude custom connectors](https://claude.com/docs/connectors/custom/remote-mcp) and [adding unlisted connectors](https://claude.com/docs/connectors/custom/add-unlisted) (the flow to match: URL → Add → Connect; org owner adds, members connect)
- [MCP security best practices](https://modelcontextprotocol.io/specification/2026-07-28/basic/security_best_practices) (SSRF, token passthrough, confused deputy)
- [OWASP SSRF prevention](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html)
- docs/UI.md "Connections" and "Toolset skipped"; T087

## Notes

- **Split into three parts** (2026-10-17), so each lands green on its own:
  1. **Guards** (done): `pkg/netguard` and pinning in `pkg/mcp/client`. Usable now for config toolsets.
  2. **Runtime API**: runtime toolsets as tenant data in the store, `POST/GET/PATCH/DELETE /v1/toolsets`, the review endpoints, `agentkit toolsets add-mcp`, every URL through `netguard`, approved fingerprints as the toolset's `pins`.
  3. **Console** on T087's design, once T087 is done.
- Part 1 decisions:
  - **The SSRF check is at connect time**, in the dialer's `Control` hook, on the address actually being connected to. A URL is never resolved to check it, so there is no window for DNS rebinding. `CheckURL` checks only what the URL itself says (scheme, user info, an IP literal). Allowed hosts (`New(allow)`) skip the address check and may use http.
  - **Refused:** loopback, private (RFC 1918, ULA), link-local (metadata), CGNAT, unspecified, multicast, broadcast, and IANA special-purpose ranges (documentation, benchmarking, reserved, Teredo, discard, site-local). IPv4-mapped, 6to4 and NAT64 addresses are judged by the IPv4 address inside; zoned addresses are refused.
  - **No proxy:** the guarded client ignores `HTTPS_PROXY`, since a proxy would make the connection and the checked address would be the proxy's. A deployment that must go through a proxy needs the proxy itself to enforce this.
  - **Redirects:** at most 5, each one through `CheckURL`, and every connection through the dialer.
  - **Pinning without the store:** each wrapped tool remembers the fingerprint the model was given. On `list_changed` the toolset rechecks, and a changed or removed tool fails its calls (`ErrToolFailed` with the reason) until its definition is back. New tools are never added at runtime. The `pins` setting (written by `agentkit mcp-tools <url>`) allows only pinned definitions at startup. Part 2 stores the approved fingerprints and passes them as `pins`.
- Mutations (each failed the package's tests): netguard: no dial-time check, no unmapping, no redirect check, no redirect cap, proxy kept, no 6to4/NAT64, no special ranges, http to any host, user info allowed, IP literal not checked, wildcard matching the apex, zone ignored. Pinning: `Invoke` ignoring a disabled tool, `Recheck` ignoring changes, a removed tool not disabled, never re-enabled, `pins` ignored, unpinned tools allowed, no recheck on `list_changed`, pin format unchecked.
- Part 2 decisions (2026-10-17):
  - **Stored as tenant data:** `store.ToolsetStore` keeps one opaque JSON record per toolset with a version for compare-and-swap, in all three backends (Postgres under RLS). `pkg/mcp/servers` owns the record's shape: the server's URL, transport, auth and headers, and per tool the approved fingerprint and definition, or a rejection, with the class an admin set and who decided when.
  - **Pinning:** a tool is offered only while the server lists it with the approved fingerprint. A new or changed tool is off and the toolset says `review_needed`; the review shows the approved definition next to the current one and the fields that differ. Approving sends the fingerprint the review showed, so a server that changes the tool in between gets `409 conflict`, never a silent approval of something unseen. A removed tool shows as `removed` without flagging a review, since nothing new would reach the model; rejecting it forgets it.
  - **Live, per turn:** the harness's registry reads the toolsets at every turn in the run's tenant, so a review, a disable or a server change takes effect at the next turn, and each call checks again that its tool is still offered. One connection per tenant and toolset, made on first use, re-made when its settings change and retried 30s after a failure; `list_changed` relists.
  - **Auth:** `none` or `headers` only. Header values are `${VAR}` references to variables the operator lists in `runtime_toolsets.header_env`; a header whose name looks like a secret must use one, so the store never holds a secret. `oauth` and `scope: user` are refused until T095 and T044 wire the flow and the vault; a server answering 401 is `422 auth_required`, naming OAuth when it advertises it.
  - **Names:** tools are always `<toolset>__<tool>`; a runtime toolset may not take a config toolset's name. Agents opt in with `runtime_toolsets` in the config (T075 makes agents data).
  - **Scope:** `agent:admin`, which `*` and `AGENTKIT_API_TOKEN` grant. Reading the review is admin too: it shows URLs and raw definitions.
  - **CLI:** `agentkit toolsets add|review|approve|reject|enable|disable|delete|list` over `pkg/agentclient`, instead of the single `add-mcp` the scope named.
  - **Not yet:** agents on a separate runner component (startup refuses `runtime_toolsets` there; the runner would need the store), OAuth and per-user connections (T095, T044), the console (part 3, after T087).
- Part 2 mutations (each failed the tests): offering a tool whose fingerprint changed, offering a disabled toolset's tools, approving without matching the live fingerprint, no per-tenant cap, connections shared across tenants, a secret header without `${VAR}`, no URL check on add (the dial-time guard alone), DELETE without the admin scope, an agent's runtime toolsets left out of the key's toolset check, invoking a tool that is no longer offered, a disabled toolset shown as ready, and serve accepting a config toolset's name as a runtime one. Store: every `RunToolsetStore` mutation was killed except tenant scoping on Postgres, which RLS covers on its own.
