---
id: T094
title: "pkg/mcp/client and the mcp toolset type: use any external MCP server's tools as tool.Tool"
epic: E19
status: todo
depends_on: [T018, T026]
---

# T094: MCP client toolset

## Context

agentkit serves MCP but cannot use other MCP servers. This task adds the client half from PLAN.md (`mcp/client`) and a toolset type `mcp`, so a server configured by URL becomes a toolset like Tavily's. Decisions: [E19](E19-external-mcp-servers.md).

## Scope

- **`pkg/mcp/client`**, on the official Go SDK's `mcp.Client`:
  - **Transports:** Streamable HTTP, falling back to the older HTTP+SSE transport; stdio from config only.
  - **Tools:** `tools/list` with pagination; each tool is wrapped as a `tool.Tool` with its input and output schemas.
  - **Calls:** `tools/call`. Results map to our tool results: text and structured content; `isError` becomes `tool.ErrToolFailed`; a protocol error is a tool error, never a panic.
  - **Limits:** per-call timeout, result size cap, `ctx` cancellation (sends `notifications/cancelled`).
  - **Reconnects** after the server drops a session.
- **Toolset type `mcp`** in `pkg/toolset` config:
  - `url`, `transport` (`auto`, `http`, `sse`, `stdio`), `command` and `args` (stdio only), `headers` (values from env vars now; from a credential slot after T044), `timeout`;
  - `tools`: an allowlist with globs (empty means all).
  - Tool names are `<toolset>__<tool>` in the registry.
- **Annotations:** `readOnlyHint`, `destructiveHint`, `openWorldHint` and `idempotentHint` map to T082's tool metadata through `tool.FromAnnotations`, as claims (E19: hints can raise risk on their own, never lower it).
- **Pinning primitives:**
  - `client.Fingerprint(tool)` is a stable SHA-256 over the name, description, input schema, output schema and annotations, in canonical JSON;
  - `client.Diff(old, new)`.
  - T096 stores fingerprints and enforces them. This task exposes `list_changed` as an event the toolset can watch.
- **Sanitizing descriptions:** strip control characters, bidi overrides and invisible Unicode (tags block, zero-width); cap the length. The raw text stays available for review.
- **Startup:** a server that doesn't answer makes the toolset **skipped** with a reason. The agent still runs with its other toolsets (UI.md "Toolset skipped").

## Out of scope

- OAuth (T095).
- Adding servers at runtime, storing fingerprints and the review UI (T096).
- The SSRF dialer (T096): config URLs come from the operator.

## Acceptance criteria

- [ ] **Round trip against our own server:** a toolset `mcp` pointed at `agentkit mcp-server` (in-process httptest) lists the demo tools with schemas and calls `calculate`.
- [ ] **Connector kit:** passes `toolsettest.RunConnector` with a fake upstream MCP server.
- [ ] **Transports:** Streamable HTTP; HTTP+SSE fallback against an SSE-only fake; stdio against a test binary.
- [ ] **Errors:**
  - `isError` results, protocol errors, timeouts, oversized results and a server that drops the session all become tool errors the model can see;
  - none of them is a 500 or a panic.
- [ ] **Cancellation:** cancelling `ctx` mid-call sends `notifications/cancelled` and returns promptly.
- [ ] **Names:** two servers that both have a `search` tool coexist as `a__search` and `b__search`.
- [ ] **Fingerprint:**
  - stable across key order and whitespace;
  - changes when the description, schema or annotations change;
  - `Diff` names the fields that changed.
- [ ] **Sanitizer:** hidden-instruction fixtures (zero-width text, tags-block text, bidi overrides) come out visible or stripped; fuzzed (`FuzzSanitizeDescription`).
- [ ] **Annotations:** a `destructiveHint` raises risk; a `readOnlyHint` alone does not lower risk below the unclassified default.
- [ ] **Skipped:** an unreachable server marks the toolset skipped with a reason, and the agent runs.
- [ ] **Mutation list** recorded.

## References

- [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk): `mcp.Client`, `StreamableClientTransport`, `SSEClientTransport`, `CommandTransport`
- MCP spec: [transports](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports), [tools](https://modelcontextprotocol.io/specification/2026-07-28/server/tools) (annotations, `list_changed`)
- Tool poisoning and rug pulls: [OWASP MCP Top 10, MCP03](https://mcpplaygroundonline.com/owasp-mcp-top-10/tool-poisoning), [pinning as the defense](https://understandingdata.com/posts/mcp-tool-poisoning/)
- T082 (tool metadata), T046 (a connector on the kit), T018 (our server, the test peer)
