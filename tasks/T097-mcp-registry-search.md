---
id: T097
title: "Search the official MCP Registry from the add form (later)"
epic: E19
status: review
depends_on: [T096]
---

# T097: MCP Registry search

## Context

Pasting a URL is the core path (T096). A search box saves looking up URLs. The official MCP Registry (`registry.modelcontextprotocol.io`, preview, API v0.1) lists servers as `server.json` metadata, some with `remotes` (hosted URLs). Decisions: [E19](E19-external-mcp-servers.md) ("Registry").

## Scope

- `GET /v1/mcp-registry/search?q=` proxies the registry's list and search API, keeps only servers with a `remotes` entry over Streamable HTTP or SSE, and caches results.
- In the console's "Add MCP server", a search box prefills the URL and name. Adding still goes through T096's probe, review and pinning: a registry entry is not a trust signal.
- A tenant setting `mcp_servers.allow` (URL globs) can limit which servers admins may add. Empty means any.
- The registry URL is configurable, so a company can point it at its own subregistry.

## Acceptance criteria

- [x] **Search:** against a recorded cassette of the registry API, search returns only entries with `remotes`; packages-only entries are dropped.
- [x] **Allowlist:** a URL outside `mcp_servers.allow` is refused by the API, not only hidden in the UI.
- [x] **Registry down:** search shows an error; adding by URL still works.

## References

- [MCP Registry](https://registry.modelcontextprotocol.io) and [registry aggregators and subregistries](https://modelcontextprotocol.io/registry/registry-aggregators)

## Notes

- **Status `review`, not `done`:** the API, the CLI and the config are built and every criterion runs against them (below). The console's search box is not: the console has no "Add MCP server" form yet (T096 part 3, after T087), so there is nothing to put it in. T096 part 3 adds it on `GET /v1/mcp-registry/search` (see "For T096 part 3"). "Search shows an error" is checked as the API's `502 registry_unavailable` and the CLI's message.
- **What was built:**
  - `pkg/mcp/registry`: a client of the registry API v0.1 (`GET /v0.1/servers?search=&limit=&version=latest&cursor=`), standard library only. It keeps servers with a `streamable-http` or `sse` remote whose URL is http(s), drops packages-only and `deleted` ones, dedupes by name, follows `nextCursor` (up to 3 pages) until it has `limit` servers, caps a page at 4 MB, and caches results (5 min, 256 searches, oldest dropped; failures not cached; copies in and out). Remote URLs with `{variables}` are kept, with the variable names, for the admin to fill in.
  - `pkg/mcp/servers`: `Allowlist` (the tenant setting `mcp_servers.allow`), `WithAllowlist`, `WithRegistry` and `Manager.Directory()` (the `agentapi.MCPRegistry`). `Add` checks the allowlist after the request's own checks and **before connecting**, so a refused URL is never probed. The search sanitizes the registry's text (`client.SanitizeDescription`, names and titles capped at 200 runes), maps transports to `AddToolsetRequest`'s (`http`, `sse`), suggests a toolset name (`app.linear/linear` is `linear`) and marks each remote `allowed`.
  - `pkg/agentapi`: `GET /v1/mcp-registry/search?q=&limit=` (scope `agent:admin`; 404 without a registry; 400 for no `q`, a `q` over 200 bytes or a `limit` outside 1..100; 502 `registry_unavailable`), `WithMCPRegistry`, `ErrURLNotAllowed` answered `403 url_not_allowed`. In the OpenAPI spec with every status probed. `pkg/agentclient`: `SearchMCPRegistry`, `ErrURLNotAllowed`, `ErrRegistryUnavailable`. CLI: `agentkit toolsets search [-limit n] <query>`.
  - Config (`runtime_toolsets`): `registry: {enabled, url}` (on by default, the official registry unless `url` names a subregistry), `mcp_servers: {allow: [...]}` for every tenant, and `tenants: {<id>: {mcp_servers: {allow: [...]}}}`. Documented in docs/QUICKSTART.md section 8.
- **Decisions:**
  - **The setting lives in the operator's config, per tenant, not in the store.** There is no tenant-settings store yet, and an allowlist that admins set for themselves through the API would bind no one but other admins. A tenant's own list replaces the default one; an empty list (the default) allows any URL. Making it tenant data that an owner sets through the API is a follow-up once a tenant-settings record exists.
  - **Pattern syntax:** `[scheme://]host[:port][/path]`. The scheme is https when left out; the host is a name, `*.domain` (subdomains, not the apex) or `*`; a missing port means the default one; a missing path (or `/*`) means any path, and `*` in a path matches anything, `/` included. The URL is parsed and matched part by part, never as a string, so `https://evil.com/x.example.com/` or `https://evil.com?.example.com/` do not match `*.example.com`. The query is not compared.
  - **The allowlist limits additions only.** Tightening it does not remove toolsets added before; an admin deletes them. A runtime check on every connection could follow if the owner wants it.
  - **Results are cached per (lowercased query, limit) for 5 minutes**, so a search done before the registry went down still answers; a new one gets 502. Failures are not cached.
  - **The registry is called with the default transport (environment proxy honored)**, not through `netguard`: its URL is the operator's, not a user's.
  - **Registry entries are not trusted:** they only prefill. The add still goes through `netguard`, the probe, the review and pinning; the search shows `allowed: false` rather than hiding entries, so an admin sees why one cannot be added.
- **Tests (criteria):**
  - Search: `TestSearchKeepsOnlyRemotes` (pkg/mcp/registry) on cassettes recorded from registry.modelcontextprotocol.io on 2026-10-22 (`testdata/linear-*.json`: 10 servers for "linear", 4 packages-only dropped), `TestSearchReadsMorePages` (cursor), `TestRegistrySearchAPI` (pkg/agentapi, same cassette through the API), `TestE2EToolsetsSearch` (the binary and the CLI).
  - Allowlist: `TestAddOutsideAllowlist` (refused, and the server got no request), `TestRegistryAllowlistAPI` (403 `url_not_allowed` from `POST /v1/toolsets`, nothing stored), `TestAllowlistPatterns`, `TestAllowlistTenants`, and the e2e test.
  - Registry down: `TestRegistryDownAPI` (502, then adding by URL works, then the search recovers), `TestSearchUnavailable`, the e2e test.
- **Mutations** (each failed a test): packages-only entries kept; SSE remotes dropped; no next page; cache never expiring; cache returning its own memory; cache size ignored; HTTP status not checked; no body cap (both the reader limit and the length check removed); wildcard host matching the apex; path, port or scheme ignored; matching on `Host` with the port; a tenant's empty list not replacing the default; `Add` skipping the allowlist; every remote `allowed`; description not sanitized; registry down answered 500; the search not admin-only; `url_not_allowed` not mapped to 403. One survivor, equivalent: the API's own `limit` range check removed (the registry client refuses the same range, still a 400). Removing only the length check also survives, since the reader limit truncates the body and the JSON fails.
- **Size** (stripped binaries, before on 0634a16, after): dev (`nosqlite`) 28,524,704 to 28,586,144 (+61 KB); local 26,935,456 to 26,996,896 (+61 KB); api (`nomcp`, which leaves out the registry client and the manager) 19,595,424 to 19,607,712 (+12 KB: the handler, wire types and the CLI); no tags 33,067,168 to 33,132,704 (+66 KB). With image ≈ binary + 3,352,416, dev goes to about 31.94 of 32 MB and api to about 22.96 of 23 MB: under budget, under 100 KB each, so no docs/SIZES.md row and no budget change; no new dependency.

## For T096 part 3 (console)

- "Find a server" before step 1 of "Add MCP server" (docs/UI.md): `GET /v1/mcp-registry/search?q=` as the admin types (debounced; results are cached server-side). Show title, `name`, sanitized description and each remote; picking one prefills `url`, `transport` and the name with `suggested_name`, and lists `headers` (a secret one as `${VAR}`) and `variables` to fill in. A remote with `allowed: false` is shown disabled with "not allowed in this workspace"; `restricted` says a list applies.
- `404` means no registry: hide the box. `502 registry_unavailable`: show "The registry can't be reached; paste the URL instead" and keep the URL field working.
- The add itself is unchanged: probe, review, pin. A registry entry is never shown as verified.
