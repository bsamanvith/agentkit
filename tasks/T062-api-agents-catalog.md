---
id: T062
title: "API: several agents, conversation listing, toolsets and tools catalog, OpenAPI spec"
epic: E11
status: todo
depends_on: [T036]
---

# T062: agents, conversations and catalog API

## Context

The console (agent picker, conversation list, toolsets page) and every other client (Claude Code, scripts, evals) need the same endpoints. Today the server runs one agent and can't list conversations or toolsets. Decisions: [E11](E11-clients.md) ("Console = API client", "Several agents").

## Scope

- **Config:**
  - `agents: [{name, model, system, toolsets, prefix_tool_names, skills?, memory?, max_turns, ...}]`, where each agent has its own harness;
  - a lone `agent` section still works, as `default`;
  - names follow the tool-name rule and are unique;
  - the agent behind a run is set by `agent` in `POST /v1/runs` (default `default`, or the only agent).
- **Conversations:**
  - each belongs to one agent;
  - continuing with a different `agent` is `409 agent_mismatch`;
  - `GET /v1/conversations?agent=&limit=&cursor=` lists the caller's conversations (id, agent, title from the first message, `created_at`, `updated_at`, status), newest first, with an opaque cursor;
  - `DELETE /v1/conversations/{id}` (in memory now; the store later).
- **Agents:** `GET /v1/agents` and `GET /v1/agents/{name}` return name, description, model id, toolsets with their tool names, skills, and memory on or off. The system prompt is returned only to `admin` (T040's scopes; until then, all callers).
- **Toolsets:**
  - `GET /v1/toolsets` returns name, type, status (`ready`, or `skipped` with the reason), MCP URL, tool count, and credential status (`configured`, `missing`, `keyless`, plus the env var name, never a value);
  - `GET /v1/toolsets/{name}/tools` returns the definitions (name, description, input and output schemas).
- **`docs/api/openapi.yaml`:** every `/v1` endpoint with request and response schemas and error codes. A test runs the server and checks that each documented route exists with the documented responses, and that no route is left out.
- **Clients:**
  - add `--agent` to `scripts/try-agent.sh`;
  - two agents in `deploy/local/agent/search.json` (a plain demo agent and a search agent);
  - update `docs/QUICKSTART.md`.

## Acceptance criteria

- [ ] **Two agents:** two configured agents run with their own system prompts and tools (a fake model records what each was given); the old single-`agent` config still works as `default`.
- [ ] **Agent mismatch:** a conversation continued with another agent gives 409 `agent_mismatch`.
- [ ] **Conversation listing:** it pages correctly (cursor, limit, newest first, no duplicates or gaps while new conversations are added); it filters by agent; it shows only the caller's tenant (with T040; until then the single tenant).
- [ ] **Toolsets:**
  - a skipped optional toolset is listed with its reason;
  - credential status shows the variable name;
  - a canary key value never appears in any response.
- [ ] **OpenAPI:** `docs/api/openapi.yaml` validates as OpenAPI 3.1; the route test fails if an endpoint is added without documenting it (checked by planting one).
- [ ] **`agentclient` (T053):** gains `Agents`, `Conversations`, `Toolsets` and `Tools` (coordinate with T053 if it is still running), and the e2e tests use them.
- [ ] **Mutation list** recorded.
