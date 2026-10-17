---
id: T094
title: "pkg/mcp/client and the mcp toolset type: use any external MCP server's tools as tool.Tool"
epic: E19
status: done
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

- [x] **Round trip against our own server:** a toolset `mcp` pointed at `agentkit mcp-server` (in-process httptest) lists the demo tools with schemas and calls `calculate`.
- [ ] ~~**Connector kit:**~~ (replaced; see Notes, "The connector kit")
  **Connector kit:** passes `toolsettest.RunConnector` with a fake upstream MCP server.
- [x] **Transports:** Streamable HTTP; HTTP+SSE fallback against an SSE-only fake; stdio against a test binary.
- [x] **Errors:**
  - `isError` results, protocol errors, timeouts, oversized results and a server that drops the session all become tool errors the model can see;
  - none of them is a 500 or a panic.
- [x] **Cancellation:** cancelling `ctx` mid-call sends `notifications/cancelled` and returns promptly.
- [x] **Names:** two servers that both have a `search` tool coexist as `a__search` and `b__search`.
- [x] **Fingerprint:**
  - stable across key order and whitespace;
  - changes when the description, schema or annotations change;
  - `Diff` names the fields that changed.
- [x] **Sanitizer:** hidden-instruction fixtures (zero-width text, tags-block text, bidi overrides) come out visible or stripped; fuzzed (`FuzzSanitizeDescription`).
- [x] **Annotations:** a `destructiveHint` raises risk; a `readOnlyHint` alone does not lower risk below the unclassified default.
- [x] **Skipped:** an unreachable server marks the toolset skipped with a reason, and the agent runs.
- [x] **Mutation list** recorded.

## References

- [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk): `mcp.Client`, `StreamableClientTransport`, `SSEClientTransport`, `CommandTransport`
- MCP spec: [transports](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports), [tools](https://modelcontextprotocol.io/specification/2026-07-28/server/tools) (annotations, `list_changed`)
- Tool poisoning and rug pulls: [OWASP MCP Top 10, MCP03](https://mcpplaygroundonline.com/owasp-mcp-top-10/tool-poisoning), [pinning as the defense](https://understandingdata.com/posts/mcp-tool-poisoning/)
- T082 (tool metadata), T046 (a connector on the kit), T018 (our server, the test peer)

## Notes

### Done (2026-10-17)

- `pkg/mcp/client`: `Connect`, `Client.Tools` (every page), `Client.Call`, `Client.Wrap` (a `tool.Tool`), `Client.ToolsChanged`, `Fingerprint`, `Diff`, `SanitizeDescription`, and `Factory`, the toolset type `mcp`, in the catalog behind `!nomcp` (`cmd/agentkit/mcpclient.go`).
- Tests (`pkg/mcp/client`): `TestRoundTripOwnServer` (agentkit's MCP server, the demo tools and `calculate`), `TestTransports` (Streamable HTTP, SSE fallback, SSE alone), `TestStdio` (the test binary as the server; it fails if any variable beyond the ones named reaches it), `TestErrors` (isError, protocol error, timeout, oversized on the wire and as JSON, non-object input), `TestReconnect` (a server that forgets the session), `TestCancel`, `TestToolsChanged`, `TestConcurrentCalls`, `TestFingerprint`, `TestSanitizeDescription`, `FuzzSanitizeDescription`, `TestAnnotations`, `TestSettings`, `TestHeaderSecret`, `TestUnreachable`. `cmd/agentkit`: `TestMCPClientNames` (two servers' `search` as `a__search` and `b__search` in one agent) and `TestE2EServeMCPClient` (the binary re-serves an external server's tools; an unreachable optional one is skipped and named in `/readyz`).

### Decisions

- **Names.** Tools keep the server's names in their toolset, and `/mcp/<name>` serves them under those names. Shadowing between servers is already impossible: an agent whose toolsets share a tool name fails at startup unless it sets `prefix_tool_names`, which names every tool `<toolset>__<tool>` (`agentapi.MergeTools`, added after this task was written). Prefixing inside the toolset as well would make `linear__linear__search`, and a factory does not know its toolset's name.
- **Allowlist.** The spec's `include` and `exclude` (globs, each must match) are the allowlist; no separate `tools` setting.
- **Headers.** Values from variables as `${NAME}`. A header whose name says it carries a secret (authorization, cookie, key, token, secret, auth, password) must use a variable. Headers go only over https, or http to loopback. A URL with user info is refused.
- **stdio.** The command gets `PATH`, `HOME` and the variables `env` names, nothing else: the server's own secrets (`ANTHROPIC_API_KEY`) never reach a third-party process.
- **Reconnects.** A call the server answers with "session not found" ran nowhere, so it goes once more on a new session. Any other broken connection (a stdio server that exited, a reset) fails that call, since it may have run, and the next call reconnects.
- **Size.** The cap is checked twice: the HTTP body of a call's answer is cut at twice the limit plus 64 KiB (content and structuredContent both carry the result), so a huge answer is never read into memory; then the result as JSON against the limit.
- **Read-only claims.** `readOnlyHint` becomes the tag `mcp.read_only_hint`, not kind `read`: a tool stays write/medium until an admin confirms (E19). `destructiveHint` raises the risk to high on its own.
- **Results.** structuredContent when the server sends it, else `{"text": ...}` with every content item (text as it is, images, audio and links as a short note).
- **Tool list changes.** A toolset's tools are fixed when it is built. `Client.ToolsChanged` signals `notifications/tools/list_changed`; the factory logs it ("restart to use the new list"). T096 builds pinning on it and on `Fingerprint`/`Diff`.
- **Lifetime.** Toolsets have no close hook, so a client lives as long as the process; its tools implement `io.Closer` for tests and for T096.
- **Size budget.** The local image's budget goes from 26 to 27 MB (docs/SIZES.md).

### The connector kit

`toolsettest.RunConnector` is built for HTTP APIs: cassettes are matched per request body, `Build` must make no request, and the error matrix is about one HTTP request and its retries. An MCP toolset must talk to the server to know its tools, and every call is a JSON-RPC exchange with a session. The concerns the kit checks are covered by this package's own tests instead: definitions valid (`Wrap` validates as a registry would), unknown settings fields, literal secrets refused, plain http refused for headers, the secret never in errors or results (`TestHeaderSecret`), server errors as tool errors, timeouts, oversized results, concurrency.

### Mutation list (each caught)

Description not sanitized; `readOnlyHint` kept as kind read; no retry when the session is lost; a literal secret header accepted; the parent's environment inherited by a stdio server; `isError` ignored; no result size check; no SSE fallback; fingerprint without annotations; headers sent over plain http to another host; tags-block characters kept; the call timeout ignored.

### For T095 and T096

- T095: `Config.HTTPClient` is where an OAuth transport goes (or the SDK's `StreamableClientTransport.OAuthHandler`, if T095 picks it); headers already come from variables.
- T096: pin with `Fingerprint` over the raw `*mcp.Tool` from `Client.Tools` (not the sanitized definition), show `Diff` and the raw description in review, and rebuild a server's tools on `ToolsChanged`. The SSRF dialer goes in `Config.HTTPClient`'s transport.

