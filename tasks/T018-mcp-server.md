---
id: T018
title: "MCP server: many toolsets on one HTTP server, one URL path each"
epic: E03
status: todo
depends_on: [T017]
---

# T018: MCP server, many toolsets per process

## Context

One process serves many toolsets (Salesforce, HubSpot, Gmail, ...), each at its own URL path, instead of one stdio server per connector. A client points at `https://host/mcp/salesforce` and sees only the Salesforce tools. Design: [PLAN.md `mcp`](../docs/PLAN.md#mcp). Uses the official Go SDK (v1.8.0 at planning time), whose `NewStreamableHTTPHandler(getServer func(*http.Request) *mcp.Server, opts)` picks an MCP server per request.

This task is only the HTTP/MCP layer. It takes already-built toolsets as input (`map[string]tool.Registry`); building them from config is T026, and wiring both into `cmd/agentkit` is T003.

## Scope

- `pkg/mcp/server`: `New(toolsets map[string]tool.Registry, opts...) (*Server, error)` returning an `http.Handler`:
  - `POST {base}/{toolset}` is the Streamable HTTP endpoint for that toolset (one `mcp.Server` per toolset, built once at startup).
  - Unknown toolset → 404. Toolset names follow the tool name rule (`^[a-zA-Z0-9_-]{1,64}$`) so they are safe as path segments.
  - `GET {base}/` lists the served toolsets (name, tool count) for discovery and debugging.
  - **Stateless mode by default** (no session store, so replicas scale behind a plain load balancer); stateful mode is an option.
  - Request body size limit (the SDK's default unless configured).
  - A `Middleware func(http.Handler) http.Handler` option for what comes later: auth, tenant resolution into `ctx`, rate limits, audit. Nothing built in yet.
- Error mapping: `ErrInvalidInput` and `ErrToolFailed` → tool result with `isError: true` (message only, never a stack trace); `ErrNotFound` → protocol error; client cancellation cancels the tool's context.
- Results: text content always; `structuredContent` too when the tool has an output schema.
- `ServeStdio(ctx, reg tool.Registry)`: one toolset over stdio, for local use with desktop clients. A thin wrapper over the same `mcp.Server` construction; not the main path.
- Example program: one process serving two toolsets (`tooltest` fixtures split into two sets) on two paths.

## Out of scope

- Building toolsets from config, and connector packages (T026, later connector epic).
- Authentication and per-tenant credentials (E07); only the middleware hook here.
- An aggregate endpoint serving all toolsets at once (add later if a client needs it, with tool names prefixed by toolset).

## Acceptance criteria

Use the SDK's own client over `httptest` (no subprocesses, no sleeps).

- [ ] Two toolsets on one server: a client at `{base}/a` lists exactly toolset `a`'s tools with their schemas, and a client at `{base}/b` exactly `b`'s. Golden file for one `tools/list` result.
- [ ] `tools/call` with valid input returns the tool's output as text and as `structuredContent` when the tool has an output schema.
- [ ] Invalid input and tool failure come back as `isError: true` results whose text has no stack trace; an unknown tool is a protocol error.
- [ ] An unknown toolset path returns 404; an invalid toolset name is rejected by `New`.
- [ ] Cancelling the client request cancels the tool's context.
- [ ] Stateless mode: two clients with no session affinity can call the same toolset concurrently (`-race`).
- [ ] Middleware option wraps every toolset endpoint (a test middleware rejecting a missing header blocks all paths).
- [ ] `ServeStdio` serves one toolset to an SDK client over an in-memory transport.
- [ ] Manual: the example server added to Claude Code as an HTTP MCP server (`claude mcp add --transport http a http://localhost:PORT/mcp/a`) lists and calls its tools. Recorded in Notes.

## Notes

- archtest allowlist: `pkg/mcp/server` → `github.com/modelcontextprotocol/go-sdk/mcp`.
