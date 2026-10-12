---
id: T063
title: "Agents over MCP: /mcp/agentkit for Claude Code and other harnesses"
epic: E11
status: todo
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

- [ ] **Through the MCP SDK client:** `agents_list` lists the configured agents; `agent_run` runs one (fake model) and returns its answer and IDs; a second `agent_run` with the `conversation_id` continues the conversation.
- [ ] **Timeout:** an `agent_run` past its timeout returns status `running` and IDs; `run_get` later shows `done`.
- [ ] **Not offered to agents:** an agent configured with the `agentkit` toolset fails startup with a clear message.
- [ ] **Auth:** once T040 is in, `/mcp/agentkit` needs credentials and a principal sees only its tenant's conversations.
- [ ] **Live (`-tags live`):** Claude Code is not available in CI, so a Go MCP client stands in: `agent_run` against real Claude with the Tavily toolset (keyless) returns an answer with a `tavily_search` call in the summary.
- [ ] **Mutation list** recorded.
