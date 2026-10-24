---
id: T063
title: "Agents over MCP: /mcp/agentkit for Claude Code and other harnesses"
epic: E11
status: done
depends_on: [T062, T053]
---

# T063: agents over MCP

## Context

The owner wants to drive and test our agents from Claude Code and other CLI harnesses. Those all speak MCP, so we expose agent operations as an MCP toolset, served by the same `mcp-server` as every other toolset. Decisions: [E11](E11-clients.md) ("Agents over MCP").

## Scope

- **A toolset factory, type `agentkit`**, built in process on the agent server: when `agent` and `mcp-server` run together, calls go in process; otherwise an `agentclient` points at a configured URL. Tools:
  - `agents_list()` returns name, description and toolsets;
  - `agent_run(agent, message, conversation_id?)` returns answer, `run_id`, `conversation_id`, status, a tool-call summary (name, short input, ok or error) and usage. It is synchronous, with a timeout setting (default 5 min); a timeout returns the IDs and status `running`, so the caller can poll;
  - `run_get(run_id)`;
  - `conversations_list(agent?, limit?)`;
  - `conversation_get(conversation_id)`.
- **Config:** `{"name": "agentkit", "type": "agentkit"}` in `mcp.toolsets`. It is not offered to agents themselves (no agent calling agents through this path; that is `flow`'s job). Startup refuses an agent listing it.
- **Docs:** add `claude mcp add --transport http agentkit http://localhost:8080/mcp/agentkit --header "Authorization: Bearer ..."` to `docs/QUICKSTART.md`, with an example session.
- Passes T049's applicable checks through the kit's fakes where it fits (it is not an outbound connector, so use `RunConnector` only if it applies; otherwise its own suite).

## References

- [MCP specification: tools](https://modelcontextprotocol.io/specification/latest/server/tools) (results, `isError`, structured content)
- `claude mcp add` docs for Claude Code (HTTP transport and headers)
- How the A2A spec shapes tasks (T067) so `agent_run`'s result can map to it later

## Acceptance criteria

- [x] **Through the MCP SDK client:** `agents_list` lists the configured agents; `agent_run` runs one (fake model) and returns its answer and IDs; a second `agent_run` with the `conversation_id` continues the conversation. `TestAgentsOverMCP` (pkg/agenttools, in process and by URL), `TestServeAgentsOverMCP` (cmd/agentkit: `agentkit serve` with `agent,mcp-server`, then a second server with `mcp-server` alone naming the first's URL).
- [x] **Timeout:** an `agent_run` past its timeout returns status `running` and IDs; `run_get` later shows `done`. `TestAgentRunTimeout` (the API there interrupts at once a run nobody follows, so it also shows the run is kept going), `TestCallTimeoutCutsTheWait`.
- [x] **Not offered to agents:** an agent configured with the `agentkit` toolset fails startup with a clear message. `TestServeAgentkitToolsetMistakes` (both config forms and the runner).
- [x] **Auth:** once T040 is in, `/mcp/agentkit` needs credentials and a principal sees only its tenant's conversations. `TestAuth` (pkg/agenttools: no key, a wrong key, a key without the toolset; tenants; a key that does not allow all of an agent's toolsets; a read-only key), `TestServeAgentsOverMCP`.
- [x] **Live (`-tags live`):** Claude Code is not available in CI, so a Go MCP client stands in: `agent_run` against real Claude with the Tavily toolset (keyless) returns an answer with a `tavily_search` call in the summary. Written (`TestLiveAgentsOverMCP`, cmd/agentkit) . **Run 2026-10-24** with the project's Anthropic key (`make test-live`, all live tests pass): `TestLiveAgentsOverMCP` passed (its search.json paths made absolute, `searchConfig`).
- [x] **Mutation list** recorded (Notes).

## Notes

### Done (2026-10-21)

- `pkg/agenttools`: the factory of type `agentkit` (`agenttools.New`, a `toolset.Factory` with `Close`) and its five tools on `tool.Func`, a client of `/v1` through `agentclient`. Settings: `url` (empty: the agent API of this process, `WithHandler`) and `timeout` (default 5m, at most 1h). `ForwardCredentials` keeps each MCP request's `Authorization` header in its context for the tools.
- In process, requests go to the agent API's handler through an `http.RoundTripper` that streams the response through a pipe (no socket, no port; the e2e tests listen on `:0`). Closing the body cancels the handler's context and waits for it to return.
- `cmd/agentkit`: `agentkit` specs are split out of `mcp.toolsets` (`splitAgentTools`): no agent, runner or toolsets component builds or serves them; the MCP server builds them once the agent API exists (serve now builds the `mcp-server` mount after the other components, at its place in the list). `agentToolsetSpecs` refuses an agent naming one. `deploy/local/agent/config.json` and `search.json` serve it at `/mcp/agentkit` (with `call_timeout` 5m30s); QUICKSTART has `claude mcp add` and an example session.

### Decisions

- **The caller's credential is forwarded, not the server's.** Both in process and by URL the agent API authenticates the MCP caller's own `Authorization` header again, so tenant, scopes and visible agents are exactly the API's (T040's rule: an agent is usable only by a key that allows all its toolsets). No service token, no separate principal hand-off. The key must also allow the toolset `agentkit` (the MCP server's access check) and have `agent:run` to run. A remote `url` must accept the same keys (with JWTs: the same issuer, and an audience it accepts).
- **`agent` is optional** in `agent_run`: empty runs the default agent, or continues the conversation's own. The API refuses a mismatch (`agent_mismatch`).
- **Waiting and the call timeout.** `agent_run` waits for `timeout`, cut to a tenth (at most 5s) before `mcp.call_timeout`, logged at startup; past it, it returns `running`, the IDs and a note to call `run_get`. The toolset keeps reading the run's events in the background until it ends (owned by the factory; `Close`, from the MCP server's `Shutdown`, stops them), because the API interrupts a run nobody follows after its reconnect grace. If the MCP caller leaves while waiting, the follower closes and the API's grace applies, as for any client.
- **A failed run is a result**, with `status` and `error`; only errors before a run starts are tool errors, carrying the API's code and message (`conversation_busy (run_id ...)`, `agent_mismatch`, `not_found`, `forbidden`).
- **Summaries are cut**: tool inputs to 200 bytes, tool results and errors to 1000, on rune boundaries, marked `…`. `conversation_get` flattens each message to text, tool calls and tool results.
- **Kinds:** `agent_run` is `execute`/`medium`; the other four `read`/`low`.
- **Not in `GET /v1/toolsets`**: the agent API lists the toolsets its agents can use, and this one is for MCP clients only.
- **`conversations_list` takes `cursor` too**, so a caller can page.

### Mutation list (each caught by a test)

1. forwarding drops the credential (`TestAuth`)
2. forwarding keeps a header the API client set itself (`TestForwarding`)
3. the stream closed when the wait ends, so no background follower (`TestAgentRunTimeout`, both modes)
4. agents may name the `agentkit` toolset (`TestServeAgentkitToolsetMistakes`)
5. every tool call reported ok (`TestToolCallSummary`)
6. cutting not on a rune boundary (`TestToolCallSummary`)
7. the wait not cut to the MCP call timeout (`TestCallTimeoutCutsTheWait`)
8. serve does not install `ForwardCredentials` (`TestServeAgentsOverMCP`)
9. `Close` does not stop the followers (`TestClose`)
10. an `agentkit` name used by another toolset too (`TestServeAgentkitToolsetMistakes`)
11. the timeout result without the conversation ID (`TestAgentRunTimeout`)
12. `conversation_get` drops tool results (`TestAgentsOverMCP`)
13. the in-process body's `Close` does not wait for the handler (`TestInProcessStreams`)
14. the API's error code dropped from tool errors (`TestRunErrors`, `TestAuth`)
15. in process ignored, so a URL always needed (`TestServeAgentsOverMCP`)
16. serve gives the MCP server no agent API (`TestServeAgentsOverMCP`)
17. the credential sent on a redirect to another host (`TestForwardingStaysAtTheAPI`)
18. agent_run's run request not ended when its caller leaves before the API answers (`TestAgentRunLeavesWithTheCaller`)

### Left

- `TestLiveAgentsOverMCP` against real Claude and Tavily: not run, no Anthropic key in the session.
- Not tried from Claude Code itself (not available here); the SDK client stands in.

### Found out of scope

- The MCP SDK client sends `"arguments": null` for a call with nil arguments, and `tool.Func` refused `null`. Fixed in review: `tool.Func` treats `null` as `{}` (`TestFuncEmptyInput`).
- `GET /v1/runs/{id}`, right as a run ends, can show the stored status (`done`) before the run's last event, so with no answer or record end (`runView`: "has just ended and its execution has not said so"). `run_get` passes that on; the tests wait for the answer.
- `deploy/k8s/base/split.yaml`'s toolsets component could serve `agentkit` with `settings.url` pointing at the API service.

### For T050 (evals) and T067 (A2A)

- `agent_run`'s result maps onto an A2A task: `run_id` the task id, `conversation_id` the context id, `status` the state (`running` → working, `done` → completed, `failed`/`canceled` → failed/canceled), `answer` the message, `tool_calls` artifacts or metadata.
- Evals can drive the agents through `/mcp/agentkit` with `pkg/agenttools`' result types (`agenttools.Run`), or call the tools in process: `agenttools.New(agenttools.WithHandler(api.Handler()))` (see its Example).

### For T064 (console admin pages)

- The toolset is not in `GET /v1/toolsets`. If the admin page should show it, the agent API needs a way to list MCP-only toolsets.
