---
id: T062
title: "API: several agents, conversation listing, toolsets and tools catalog, OpenAPI spec"
epic: E11
status: done
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

## References

- [OpenAPI 3.1](https://spec.openapis.org/oas/v3.1.0)
- [Google AIP-158: Pagination](https://google.aip.dev/158) (opaque page tokens)
- How the Claude API and OpenAI Assistants/Responses APIs list conversations and models (shape and pagination), for familiar shapes

## Acceptance criteria

- [x] **Two agents:** two configured agents run with their own system prompts and tools (a fake model records what each was given); the old single-`agent` config still works as `default`. — `TestAgentsRunWithTheirOwnHarness`, `TestDefaultAgent` (pkg/agentapi); `TestServeTwoAgents`, `TestServeLegacyAgentConfig`, `TestLoadServeConfigForms`, `TestServeRejectsBadSetup` (the `agents:` rows), and the unchanged `TestE2EServeAgent` on the legacy `config.json` (cmd/agentkit).
- [x] **Agent mismatch:** a conversation continued with another agent gives 409 `agent_mismatch`. — `TestAgentMismatch`, `TestServeTwoAgents` (`errors.Is(err, agentclient.ErrAgentMismatch)`), `TestOpenAPIResponses` (the 409 probe).
- [x] **Conversation listing:** pages correctly (cursor, limit, newest first, no duplicates or gaps while new conversations are added); filters by agent; shows only the caller's tenant (single tenant until T040). — `TestConversationListPages`, `TestConversationListFiltersByAgent`, `TestConversationsAreTheCallersTenant`, `TestDeleteConversation`, `FuzzConversationList`, and the rig's `listAll` invariant after every pkg/agentapi test.
- [x] **Toolsets:**
  - a skipped optional toolset is listed with its reason — `TestToolsetsCatalog`, `TestE2EServeCatalog`;
  - credential status shows the variable name — `TestE2EServeCatalog` (`TAVILY_API_KEY`, `EXA_API_KEY` configured; `MY_PARALLEL_KEY` missing, from `api_key_env`), `TestToolsetCatalogCutsKeys`;
  - a canary key value never appears in any response — `TestE2EServeCatalog` (the binary, every body recorded, three key canaries and the token), `TestToolsetCatalogCutsKeys` (a skip reason quoting the key).
- [x] **OpenAPI:** `docs/api/openapi.yaml` validates as OpenAPI 3.1; the route test fails if an endpoint is added without documenting it (checked by planting one). — `TestOpenAPISpec` (structure, `toolsettest.LoadOpenAPI`, routes both ways; plants `GET /v1/planted` and drops `DELETE`), `TestOpenAPIResponses` (every documented status of every route is produced and its body, headers and SSE events validate against a sealed copy of the spec).
- [x] **`agentclient` (T053):** gains `Agents`, `Conversations`, `Toolsets` and `Tools` (plus `Agent`, `DeleteConversation`, `MCPURL`, `ErrAgentMismatch`), and the e2e tests use them. — `TestCatalog`, `TestRequests`, `TestAPIErrors` (pkg/agentclient); `TestE2EServeAgent`, `TestE2EServeCatalog`, `TestServeTwoAgents`.
- [x] **Mutation list** recorded (Notes).

## Notes

### Files

- **`pkg/agentapi`**:
  - `catalog.go` (new): `Agent`, `Toolset`, `Credential`, `NewAgents`, `WithToolsets`, `WithTenant`, the agents, toolsets and conversation-list handlers, `DELETE /v1/conversations/{id}`, the cursor;
  - `server.go`: a conversation and run carry their agent; `POST /v1/runs` picks it; the route table (`Server.Routes`) that the OpenAPI test reads; `New(h)` is `NewAgents` with one agent named `default`;
  - `options.go`, `wire.go`, `doc.go`: the matching aliases and docs.
- **`pkg/agentapi/wire`**: `catalog.go` (new) holds `Agent`, `Model`, `AgentToolset`, `AgentList`, `ConversationSummary`, `ConversationList`, `Toolset`, `Credential`, `ToolsetList`, `Tool`, `ToolList`, `CodeAgentMismatch` and the status constants; `wire.go` adds `agent` to `Run`, `Conversation` and `RunRequest`. Still imports only `pkg/llm`.
- **Goldens:** `tool_run.run.json` and `tool_run.conversation.json` gain `"agent": "default"`, an additive field.
- **Fuzz:**
  - `FuzzPostRun`'s oracle now counts a run naming another agent as invalid (404);
  - `FuzzConversationList` is new.
- **`cmd/agentkit`**:
  - config (`serve.go`): `agents` and `agent_api` sections;
  - `agent.go`: one harness per agent; the toolsets catalog (`toolsetCatalog`, `credentialOf`, `credentialEnv`).
- **`docs/api/openapi.yaml`**: new.
- **`pkg/agentclient`**:
  - `catalog.go`: new;
  - `ErrAgentMismatch`;
  - docs.
- **Scripts, config and docs:**
  - `scripts/try-agent.sh --agent`;
  - `deploy/local/agent/search.json` (two agents);
  - `docs/QUICKSTART.md` (sections 3, 5, 6).
- **Not changed:** `internal/archtest`, `pkg/harness`, `pkg/toolset`, `deploy/local/agent/config.json` (kept in the one-agent form so the legacy path stays exercised end to end). No new dependency.

### Config

```json
{
  "mcp": {"toolsets": [{"name": "demo", "type": "demo"}, {"name": "tavily", "type": "tavily", "optional": true}]},
  "agent_api": {"max_concurrent_runs": 16, "run_timeout": "10m", "max_runs": 1000, "max_conversations": 1000, "retention": "24h", "max_body_bytes": 1048576},
  "agents": [
    {"name": "default", "description": "...", "model": {"id": "claude-opus-5-5", "effort": "low"}, "system": "...", "toolsets": ["demo"], "max_turns": 10},
    {"name": "search",  "description": "...", "system": "...", "toolsets": ["tavily"], "prefix_tool_names": false, "max_total_tokens": 200000}
  ]
}
```

- **Per agent:** `name` (required, `^[a-zA-Z0-9_-]{1,64}$`, unique), `description`, `model{provider,id,effort,max_tokens}`, `system`, `toolsets`, `prefix_tool_names`, `max_turns`, `max_total_tokens`.
- **`agent_api`:** server-wide limits only; a server limit inside an agent, or an agent field inside `agent_api`, is an unknown field and fails startup.
- **The one-agent form:**
  - an `agent` section (agent fields plus API limits, as before T062) is served as the agent `default`;
  - with no section at all, the server runs one `default` agent with the default model and prompt;
  - `agent` together with `agents` or `agent_api` is an error, and so is `agents: []`.
- **Errors name the agent:** `agents.search.toolsets names "exa", which is not in mcp.toolsets`, `agents.b.model.provider`, `agents[1].name` (duplicate). The one-agent form keeps the old `agent.` labels.
- **Toolsets built:** with only the agent component, only the toolsets some agent uses are built. With the MCP server, every `mcp.toolsets` entry is built, as before.

### Endpoints (for T055/T064)

All need `Authorization: Bearer $AGENTKIT_API_TOKEN` when a token is set. Every body has `"v": 1`, and fields are additive: clients ignore unknown ones. The full schemas, statuses and error codes are in `docs/api/openapi.yaml`.

- **`GET /v1/agents`** returns `{"v":1,"agents":[Agent...]}` in config order. The console's agent picker reads `default` to preselect.

  ```json
  {"v":1,"name":"search","description":"A research assistant that searches the web and cites its sources.","default":false,
   "model":{"provider":"anthropic","id":"claude-opus-5-5","effort":"low"},
   "toolsets":[{"name":"tavily","status":"ready","tools":["tavily_extract","tavily_search"]},
               {"name":"exa","status":"skipped","tools":[]}],
   "skills":[],"memory":false}
  ```

  Tool names are as the model sees them, with the `<toolset>__` prefix when `prefix_tool_names` is set.
- **`GET /v1/agents/{name}`** returns the same object plus `"system"`, or 404 `not_found`.
- **`GET /v1/conversations?agent=&limit=&cursor=`** returns the caller's conversations, newest first by creation:

  ```json
  {"v":1,"conversations":[{"id":"conv_7","agent":"search","title":"What is the latest Go release? Cite the source.",
     "status":"idle","message_count":4,"created_at":"2026-10-12T09:00:00Z","updated_at":"2026-10-12T09:00:41Z"}],
   "next_cursor":"Y29udmVyc2F0aW9ucy1iZWZvcmU6Nw"}
  ```

  - `limit` is 1 to 100, default 20. A bad limit or cursor is 400 `invalid_request`; an unknown `agent` is 404.
  - `next_cursor` is present only when more follow, and the cursor is opaque.
  - The list is ordered by creation, not by update. Conversations started while a client pages land before its first page, so following cursors sees every older one exactly once. To refresh the list, fetch the first page again.
  - `status` is `idle`, `running` or `interrupted`.
  - `title` is the first message on one line, cut to 80 characters with `…`.
- **`DELETE /v1/conversations/{id}`** returns 204 with no body. The conversation and its runs are gone, so `GET` on either is then 404. A conversation with a run going on is 409 `conversation_busy` (retryable); unknown or another tenant's is 404.
- **`GET /v1/toolsets`** returns `{"v":1,"toolsets":[Toolset...]}` in `mcp.toolsets` order:

  ```json
  {"name":"exa","type":"exa","status":"skipped",
   "reason":"toolset \"exa\" (type \"exa\"): exa API key not set: environment variable EXA_API_KEY is unset or empty (...)",
   "mcp_path":"/mcp/exa","tool_count":0,"credential":{"status":"missing","env":"EXA_API_KEY"}}
  ```

  - `credential.status`:

    | Status | Meaning |
    |---|---|
    | `configured` | the variable is set |
    | `missing` | the toolset needs it |
    | `keyless` | Tavily without a key |
    | `not_needed` | demo; there is no `env` |

  - `mcp_path` is absent when the MCP server component is not running.
- **`GET /v1/toolsets/{name}/tools`** returns `{"v":1,"toolset":"demo","status":"ready","tools":[{"name","description","input_schema","output_schema"?}]}`. A skipped toolset is 200 with `status: skipped` and `tools: []`; an unknown one is 404.
- **`POST /v1/runs`** takes an optional `"agent"`:
  - a new conversation gets the agent named, or the default: the only agent, else the one named `default`. With several agents and no `default` it is 400 `invalid_request`, naming them.
  - an unknown agent is 404 `not_found`.
  - continuing a conversation with `agent` omitted keeps its agent; naming another is 409 `agent_mismatch`.
  - `Run` and `Conversation` now carry `"agent"`.
- **Go:** `agentclient.Agents`, `Agent`, `Conversations(ConversationsQuery{Agent, Limit, Cursor})`, `DeleteConversation`, `Toolsets`, `Tools`, `MCPURL(toolset)` (resolves `mcp_path` against the client's base URL, or `""`), and `ErrAgentMismatch`.

### For T063 (agents over MCP)

The four MCP tools map onto the endpoints one to one, and their schemas can be lifted from the OpenAPI components:

| MCP tool | Endpoint | Schema |
|---|---|---|
| `agents_list` | `GET /v1/agents` | `AgentList` |
| `agent_run` | `POST /v1/runs` with `Accept: application/json` | `RunRequest` → `Run` |
| `conversations_list` | `GET /v1/conversations` | `ConversationList` |
| `conversation_get` | `GET /v1/conversations/{id}` | `Conversation` |

- **How to call them:** in process, the cleanest is `agentclient` pointed at the server's own address with the server token. Calling `pkg/agentapi` handlers directly would skip auth and the tenant.
- **Tenant:** carry it over. `WithTenant(func(*http.Request) string)` is the hook T040 fills, and the MCP side must pass the same principal so an MCP caller sees only its own conversations.
- **`agent_run`'s tool-call summary:** it is in `Run.record.turns[].tool_calls`.
- **Errors:** `409 agent_mismatch` and `conversation_busy` should surface as tool errors with the API's message, which already says what to do.

### Decisions

- **The system prompt is shown to everyone for now.** `GET /v1/agents/{name}` returns `system` to every caller; `GET /v1/agents` never does. `// TODO(T040)` in `getAgent`: make it admin-only once the API has roles.
- **`mcp_path`, not a URL.** The server cannot know the URL clients reach it by (proxies, Docker port maps), so it reports the path and `agentclient.MCPURL` joins it to the client's base URL.
- **No credential values anywhere.**
  - A credential is listed only by its variable's name.
  - The variable comes from `settings.api_key_env`, else a per-type table in `cmd/agentkit` (`credentialEnv`: tavily, exa, parallel). `// TODO`: move it into the toolset factories so a new connector declares its own.
  - As defence in depth, any value of a listed variable is cut out of skip reasons (`[redacted]`).
- **Conversation order is creation order**, not last update. That is the only order in which a cursor gives neither duplicates nor gaps while conversations change, and the task asks for that guarantee. A "recently active" view can sort a page by `updated_at` client-side.
- **The cursor** is base64url of `conversations-before:<seq>`, decoded strictly: only a cursor this server produced is accepted, anything else is 400. It survives deletions, since it is a position and not an ID.
- **`DELETE` and tenants.** Another tenant's conversation is 404 on every route, never 403, so IDs don't leak. Without `WithTenant` (until T040) every request is one tenant. When T040 mounts `pkg/auth`'s middleware (T039) in front of `/v1`, it passes `agentapi.WithTenant(func(r *http.Request) string { t, _ := tenancy.From(r.Context()); return t.ID() })`. The tests already drive this hook with a header (`TestConversationsAreTheCallersTenant`).
- **OpenAPI written as JSON.** The file is YAML 1.2 written in JSON syntax, because the only spec loader in the module (`toolsettest.LoadOpenAPI`) reads JSON. Moving it to block YAML needs a YAML library, so a `rules.go` allowlist entry and an owner's OK.
  - Responses are inlined, not `$ref`s, because the loader only resolves `#/components/schemas`.
  - The published spec leaves objects open, since the API grows. `TestOpenAPIResponses` seals a copy (`additionalProperties: false` on every component object), so an undocumented field fails the test without binding clients.
- **Limits of the 3.1 validation.** There is no OpenAPI meta-schema validator in the module. `TestOpenAPISpec` checks the structure:
  - `openapi: 3.1.0`, `info`, `security`;
  - unique `operationId`s;
  - path parameters declared and required;
  - numeric statuses;
  - every schema compiling under JSON Schema 2020-12 via `LoadOpenAPI`.

  Then `TestOpenAPIResponses` checks the spec against real traffic. It passes on the wire after PR #4 (lenient message decoding), so the message schemas match what the server sends today.

### Invariants (checked after every pkg/agentapi test, in `rig.check`)

- Listing every conversation (limit 7, following cursors) gives each one tracked once, with its agent, status and message count:
  - no duplicates;
  - newest first;
  - a page with a `next_cursor` is full and the next page is not empty;
  - a page of `limit=n` over n conversations has no cursor.
- What each agent's model received is a prefix-consistent sequence, as before, checked over every agent's model.

### Mutation check

Each mutant was planted alone. The driver ran `go test -count=1` on the package it touches, `./pkg/agentapi/` or `./cmd/agentkit/` (fuzz targets on their seed corpus), and restored the file after each run. 21 mutants, 21 caught.

- **Survivor.** "`next_cursor` when exactly `limit` remain" first survived. The fix was general: the `listAll` invariant above, not a test for that case.
- **Fixes to the mutants themselves.** Four mutants first failed to compile and were rewritten so that only the behavior changes.
- **Recheck.** The whole list was run again after that.

| Mutant | Caught by |
|---|---|
| tenant filter dropped from the listing | `TestConversationsAreTheCallersTenant` |
| cursor `<=` instead of `<` | `TestConversationListPages`, `TestConversationListFiltersByAgent`, `TestConcurrentConversationsDoNotInterfere` |
| no `agent_mismatch` check | `TestAgentMismatch`, `TestOpenAPIResponses` |
| credential reports the variable's value | `TestE2EServeCatalog`, `TestToolsetCatalogCutsKeys` |
| a route served but not documented | `TestOpenAPISpec` |
| title not truncated | `TestConversationListPages` |
| delete ignores a running run | `TestDeleteConversation`, `TestOpenAPIResponses` |
| delete keeps the conversation's runs | `TestDeleteConversation` |
| delete ignores the tenant | `TestConversationsAreTheCallersTenant` |
| default agent is the first, not `default` | `TestDefaultAgent` |
| `next_cursor` when exactly `limit` remain | the `listAll` invariant (every test using the rig), `TestConversationListPages` |
| agent filter dropped | `TestConversationListFiltersByAgent` |
| listing oldest first | `TestConversationListPages`, `FuzzConversationList`, `TestRetention`, and 2 more |
| `GET /v1/agents/{name}` without the system prompt | `TestAgentsRunWithTheirOwnHarness` |
| skipped toolset reported ready | `TestToolsetsCatalog` |
| `prefix_tool_names` not applied in the agent view | `TestAgentsRunWithTheirOwnHarness` |
| skip reason dropped (cmd) | `TestE2EServeCatalog` |
| key not cut out of a skip reason | `TestToolsetCatalogCutsKeys` |
| wrong MCP path | `TestE2EServeCatalog`, `TestE2EServeAgent` |
| missing key reported configured | `TestE2EServeCatalog` |
| bad cursor read as "from the start" | `TestConversationListPages` |

### Live

Not run. This session has no `ANTHROPIC_KEY`, so `make test-live` is left to the planner. Nothing live-only changed: the model adapter is untouched, and each agent's model is built by the same `newModel` path the live tests use.
