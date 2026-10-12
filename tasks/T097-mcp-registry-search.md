---
id: T097
title: "Search the official MCP Registry from the add form (later)"
epic: E19
status: todo
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

- [ ] **Search:** against a recorded cassette of the registry API, search returns only entries with `remotes`; packages-only entries are dropped.
- [ ] **Allowlist:** a URL outside `mcp_servers.allow` is refused by the API, not only hidden in the UI.
- [ ] **Registry down:** search shows an error; adding by URL still works.

## References

- [MCP Registry](https://registry.modelcontextprotocol.io) and [registry aggregators and subregistries](https://modelcontextprotocol.io/registry/registry-aggregators)
