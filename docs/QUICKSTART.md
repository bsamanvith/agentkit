# Quickstart: run the agent server locally

One process serves two things:

- the **agent API**: `POST /v1/runs` sends a prompt to one of the configured agents (Claude with its own system prompt and tools from the configured toolsets, run by our harness); the reply streams back as Server-Sent Events, and follow-ups continue the conversation. `GET /v1/agents`, `/v1/conversations` and `/v1/toolsets` list what the server has;
- the same **toolsets over MCP** at `/mcp/<name>`, for Claude Code, Codex or any MCP client.

Runs and conversations are kept in memory by default (lost on restart); `AGENTKIT_STORE=sqlite:/path/agentkit.db` or a `postgres://` URL keeps them (section 5).

## 1. Prerequisites (macOS)

- An Anthropic API key, exported as `ANTHROPIC_API_KEY` (or `ANTHROPIC_KEY`).
- Either **Go** (`brew install go`; any recent version, the right toolchain is fetched automatically from `go.mod`) or **Docker Desktop** for the container.
- `curl` (built in).

## 2. One command

```sh
git clone https://github.com/bsamanvith/agentkit && cd agentkit   # or: git pull
export ANTHROPIC_API_KEY=sk-ant-...
scripts/try-agent.sh            # build, start, run a short demo, keep serving (Ctrl-C stops)
```

Variants: `scripts/try-agent.sh --demo` runs the demo and stops; `scripts/try-agent.sh --docker` does the same with the container image (`make image`). `PORT=9090` changes the port (default 8080).

No key yet? `scripts/smoke.sh --keep` runs the split deployment (API and console, runner, toolsets) as processes on a fake model, checks it with the smoke suite, and keeps serving the console on http://127.0.0.1:18480 with the token it prints. Say "echo" to see a tool call.

The demo shows:

1. a streamed run where Claude uses the `calculate` tool (17 × 23 = 391), with its events: `run_start`, `tool_call`, `tool_result`, `turn_done`, `text_delta`, `turn_done`, `run_done`;
2. a follow-up in the same conversation, as JSON (`400`);
3. the same tools listed over MCP at `/mcp/demo` (`calculate`, `current_time`, `echo`);
4. a request without the token refused with 401.

It then prints the token and ready-to-paste commands. Open http://localhost:8080 to chat in the browser: the console asks for the token once per tab.

## 3. By hand

```sh
go build -o bin/agentkit ./cmd/agentkit
export AGENTKIT_API_TOKEN=$(openssl rand -hex 16)
AGENTKIT_COMPONENTS=agent,web,mcp-server bin/agentkit serve -config deploy/local/agent/config.json
```

In another terminal (same `AGENTKIT_API_TOKEN`):

```sh
# stream a run (SSE)
curl -sN localhost:8080/v1/runs -H "Authorization: Bearer $AGENTKIT_API_TOKEN" \
  -d '{"message":"What time is it? Use a tool."}'

# wait for the result as JSON instead
curl -s localhost:8080/v1/runs -H "Authorization: Bearer $AGENTKIT_API_TOKEN" \
  -H 'Accept: application/json' -d '{"message":"What is 6 * 7? Use a tool."}'

# continue a conversation: pass the conversation_id from run_start
curl -s localhost:8080/v1/runs -H "Authorization: Bearer $AGENTKIT_API_TOKEN" \
  -H 'Accept: application/json' -d '{"message":"Double it.","conversation_id":"conv_..."}'

# read a run or a whole conversation
curl -s localhost:8080/v1/runs/run_...          -H "Authorization: Bearer $AGENTKIT_API_TOKEN"
curl -s localhost:8080/v1/conversations/conv_... -H "Authorization: Bearer $AGENTKIT_API_TOKEN"

# create an empty conversation first (its ID before any message: what a UI page needs), then run in it
curl -s -X POST localhost:8080/v1/conversations -H "Authorization: Bearer $AGENTKIT_API_TOKEN" -d '{"agent":"default"}'
curl -sN localhost:8080/v1/runs -H "Authorization: Bearer $AGENTKIT_API_TOKEN" -d '{"message":"Hi","conversation_id":"conv_..."}'

# list the conversations, newest first (next page: ?cursor=<next_cursor>), and delete one
curl -s 'localhost:8080/v1/conversations?limit=10' -H "Authorization: Bearer $AGENTKIT_API_TOKEN"
curl -s -X DELETE localhost:8080/v1/conversations/conv_... -H "Authorization: Bearer $AGENTKIT_API_TOKEN"

# the agents (a run picks one with "agent"; GET /v1/agents/<name> adds its system prompt)
curl -s localhost:8080/v1/agents -H "Authorization: Bearer $AGENTKIT_API_TOKEN"

# the toolsets: ready or skipped (and why), the MCP path, which key variable each reads
curl -s localhost:8080/v1/toolsets -H "Authorization: Bearer $AGENTKIT_API_TOKEN"
curl -s localhost:8080/v1/toolsets/demo/tools -H "Authorization: Bearer $AGENTKIT_API_TOKEN"

# health, readiness, version (no token)
curl -s localhost:8080/healthz; curl -s localhost:8080/readyz; curl -s localhost:8080/version
```

Use the tools from Claude Code: `claude mcp add --transport http agentkit-demo http://localhost:8080/mcp/demo --header "Authorization: Bearer $AGENTKIT_API_TOKEN"`. When `AGENTKIT_API_TOKEN` is set, `/mcp` needs it too.

### From Claude Code: the agents over MCP

The toolset of type `agentkit` serves the agents themselves as MCP tools, so Claude Code (or any MCP client) can run and test them. `config.json` and `search.json` serve it at `/mcp/agentkit`:

```sh
claude mcp add --transport http agentkit http://localhost:8080/mcp/agentkit --header "Authorization: Bearer $AGENTKIT_API_TOKEN"
```

| Tool | Does |
|---|---|
| `agents_list` | the agents you may run: name, description, default, toolsets |
| `agent_run(agent?, message, conversation_id?)` | runs an agent and waits for it: `answer`, `run_id`, `conversation_id`, `status`, `tool_calls` (name, short input, ok or error) and `usage`; `conversation_id` continues a conversation |
| `run_get(run_id)` | a run, as `agent_run` returns it |
| `conversations_list(agent?, limit?, cursor?)` | your conversations, newest first |
| `conversation_get(conversation_id)` | a conversation's messages, with their tool calls and results |

An example session in Claude Code:

```text
> Use the agentkit tools: ask the search agent what the latest Go release is, then ask it for the release date in the same conversation.

● agentkit - agent_run (MCP)(agent: "search", message: "What is the latest stable Go release?")
  ⎿ {"run_id": "run_…", "conversation_id": "conv_…", "agent": "search", "status": "done",
     "answer": "Go 1.27.2 … (https://go.dev/doc/devel/release)",
     "tool_calls": [{"name": "tavily_search", "input": "{\"query\":\"latest Go release\"}", "ok": true}],
     "usage": {"input_tokens": 2310, "output_tokens": 96}, "turns": 2}

● agentkit - agent_run (MCP)(message: "When was it released?", conversation_id: "conv_…")
  ⎿ {"status": "done", "answer": "It was released on …", …}
```

- **Waiting.** `agent_run` waits up to the toolset's `timeout` setting (default `5m`), and stops a little before `mcp.call_timeout` (default `30s`; the local configs set `5m30s`). A run still going then is not stopped: the result has `"status": "running"` and its IDs, and `run_get` shows it end.
- **Who you are.** Each call goes to the agent API with your own key, so you see what the API shows that key: your tenant's conversations, and only the agents whose toolsets the key allows all of. The key must also allow the toolset `agentkit` itself, and needs the scope `agent:run` to run agents (`agent:read` to only list and read).
- **Not for agents.** An agent that lists an `agentkit` toolset fails startup; agents calling agents is what flows are for.
- **Split deployment.** When `mcp-server` runs without the `agent` component, give the toolset the agent API's URL: `{"name": "agentkit", "type": "agentkit", "settings": {"url": "http://agent:8080"}}`. Each caller's `Authorization` header is sent on to that URL, so both must accept the same keys.

### Other teams' agents: the `a2a` toolset

A toolset of type `a2a` gives an agent one tool per remote [A2A](https://a2a-protocol.org) agent, `ask_<name>`, described from the agent's card:

```json
{"name": "partners", "type": "a2a", "settings": {"agents": [
  {"name": "billing", "card_url": "https://billing.example.com/.well-known/agent-card.json", "token_env": "BILLING_A2A_TOKEN", "timeout": "5m"}
]}}
```

The credential (from `token_env`, or the toolset's `credential` slot, sent as a bearer token or in `header`) goes to the card's origin only; a card whose interfaces are elsewhere is refused, and redirects are not followed. A call waits up to `timeout` (default `2m`); a task still working then comes back with its `task_id`, which the model passes again to keep waiting. Results carry a `notice` that they are third-party data. Not in the runner and toolsets images (`noa2a`). In Go flows, `pkg/a2a/a2astep` runs a remote agent as a step that resumes its task by ID.

### From the terminal: `agentkit chat`, `agentkit ask` and `agentkit create`

The same binary is a client of the agent API. It reads the server from `-server` or `AGENTKIT_SERVER` (default `http://127.0.0.1:8080`) and the token from `AGENTKIT_API_TOKEN`, and never prints the token.

```sh
# talk to an agent: answers stream in, with each tool call and its result, then the usage
bin/agentkit chat                       # -agent search picks another agent; -conversation conv_... continues one
```

In `chat`:
- `/new` starts a new conversation, `/conv` prints its ID, `/history` shows it, `/details` shows the last tool call and its result in full, and `/quit` (or Ctrl-D) quits.
- Ctrl-C while an answer is coming cancels the run on the server and returns to the prompt. If the run had not answered yet, Enter on an empty line resumes it.
- Ctrl-C at the prompt quits.

```sh
# one run, for scripts: prints the answer only (-v adds the conversation, tool calls and usage on stderr)
bin/agentkit ask "What is 17 * 23? Use a tool."
echo "Double it." | bin/agentkit ask -conversation conv_... -     # the message from stdin

# headless: every event of the run as one JSON line (the SSE data, unchanged)
bin/agentkit ask -json -agent search "What is the latest Go release? Cite the source." | jq -c '{type, name}'
```

`ask` exits with:

| Code | Meaning |
|---|---|
| 0 | `run_done` |
| 1 | `run_error` (its code is on stderr) |
| 2 | usage, HTTP or stream error |
| 3 | `conversation_busy`, `over_capacity` or `shutting_down` |
| 130 | interrupted |

For code 3, `-retry N` waits the server's `Retry-After` (1 s for a busy conversation) and tries again, up to N times.

A conversation can also be created before its first message; `agentkit create` prints its ID (`-json`: the conversation):

```sh
conv=$(bin/agentkit create -agent search)
bin/agentkit ask -conversation "$conv" "What is the latest Go release?"
bin/agentkit chat -conversation "$conv"
```

## 4. Container

```sh
make image
docker run --rm --stop-timeout 20 -p 8080:8080 \
  -e ANTHROPIC_API_KEY -e AGENTKIT_API_TOKEN \
  -e AGENTKIT_COMPONENTS=agent,web,mcp-server \
  -v "$PWD/deploy/local/agent/config.json:/etc/agentkit/config.json:ro" \
  agentkit:latest
```

Three builds of the image, by the stores and bus they hold:

| Command | Image | Stores and bus | Size |
|---|---|---|---|
| `make image-local` | `agentkit-local` | memory or SQLite, one process; no Postgres, Redis or `migrate` | 24.3 MB |
| `make image-dev` | `agentkit-dev` | memory or Postgres, and Redis for several replicas; no SQLite | 26.1 MB |
| `make image` | `agentkit` | all of them | 30.4 MB |

A backend an image leaves out fails at startup and says so (`AGENTKIT_STORE: this agentkit is built without SQLite`).

Inside the container the server listens on `:8080` (not loopback), so `AGENTKIT_API_TOKEN` is required. `--stop-timeout 20` lets runs drain within the 15 s shutdown deadline.

## 5. Configuration

`deploy/local/agent/config.json` is the starting point: one agent. `deploy/local/agent/search.json` has three (sections 6 and 7). A server runs one or more agents, each with its own model, system prompt and toolsets, listed in `agents`; the API's limits are in `agent_api`. All fields are optional except `name`; unknown fields fail startup.

```json
{
  "mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
  "agent_api": {"max_concurrent_runs": 16, "run_timeout": "10m"},
  "agents": [
    {"name": "default", "description": "A plain assistant.", "system": "Keep answers short.", "toolsets": ["demo"]},
    {"name": "math", "model": {"effort": "high"}, "system": "You do arithmetic with the calculate tool.", "toolsets": ["demo"]}
  ]
}
```

**Other models.** `"provider": "openai"` runs an agent on any server that speaks OpenAI's Chat Completions API: OpenAI, vLLM, Ollama, llama.cpp, Groq, or a gateway such as LiteLLM or OpenRouter. Ollama on the same machine needs no key:

```json
{"name": "local", "model": {"provider": "openai", "id": "llama3.2", "base_url": "http://localhost:11434/v1", "context_window": 128000}}
```

The key is read on the process that runs the agent (the runner, when split) from the variable `api_key_env` names, `OPENAI_API_KEY` by default; if it is unset, no key is sent. Plain `http://` base URLs work, with a key too, for a server on the same host or inside a cluster (`http://vllm:8000/v1`); the key then crosses the network unencrypted, so use `https://` anywhere else. Set `model.context_window` to the model's window: the default, 200000, is Claude's.

A run names its agent (`{"message": "...", "agent": "math"}`); without one it gets the agent named `default` (or the only agent). A conversation stays with its agent: continuing it with another is `409 agent_mismatch`.

Each entry of `agents`:

| Field | Default | Meaning |
|---|---|---|
| `name` | required | `^[a-zA-Z0-9_-]{1,64}$`, unique |
| `description` | none | shown in `GET /v1/agents` |
| `model.provider` | `anthropic` | `anthropic` (Claude), or `openai` for any OpenAI-compatible server (below) |
| `model.id` | `claude-haiku-5-5` for `anthropic`; required for `openai` | model ID, as the server names it |
| `model.base_url`, `model.api_key_env` | `$OPENAI_BASE_URL` or OpenAI; `OPENAI_API_KEY` | `openai` only: the server, ending before `/chat/completions`, and the variable holding its key |
| `model.effort` | adapter default (medium) | `low` … `max`; for `openai`, sent as `reasoning_effort` |
| `model.max_tokens` | adapter default | output limit per model call |
| `model.context_window`, `model.compact_at` | 200000, 0.8 | when a request fills this share of the window, the conversation is compacted (summarized) before its next run; a request that does not fit at all is compacted and retried once |
| `models` | none (`model` alone) | other models the agent may run, which each run picks (`POST /v1/runs` `"model"`, `agentkit ask -model`; below, "Choosing the model") |
| `system` | a short generic prompt | system prompt |
| `toolsets` | none | names from `mcp.toolsets` the agent may use |
| `prefix_tool_names` | false | name tools `<toolset>__<tool>` (needed when two toolsets share a tool name) |
| `tool_descriptions` | none | descriptions this agent gives its tools instead of the toolset's, by the name its model sees (`{"web_search": "Search only for prices."}`); at most 4 KiB each, schemas unchanged. A variant of `models` can add its own, which win on its models |
| `runtime_toolsets` | none | toolsets admins add while the server runs (section 8); the agent gets their approved tools as `<toolset>__<tool>` |
| `skills` | none | folders of Agent Skills (`"path"` or `{"path": ..., "optional": true}`), relative to the config file (section 7, "Skills") |
| `defer_loading` | none | tools the model is not sent until it finds them: `true` for every toolset tool, or a list of toolset names and tool-name globs (section 7, "Deferred tools") |
| `model.tool_search` | `native` for `anthropic`, `emulated` for `openai` | who holds deferred tools back: the Claude API (`native`), or agentkit (`emulated`) |
| `max_turns`, `max_total_tokens` | 25, unlimited | budgets per run |
| `tool_policy` | `all` | which tool calls the agent may make (below) |
| `interaction` | `autonomous` | whether its tool calls may wait for people: `interactive` or `supervised` (an `ask_user` question, or a call the policy wants approved, suspends the run until answered), or `autonomous` (never: `ask_user` is not offered, and such a call is refused) |
| `isolation` | `process` | the least isolation the agent's runs need: `process`, `sandbox` or `vm`; a run goes only to a runner pool at least that isolated (`runners`, below), and startup fails when there is none. Only `process` pools exist yet |
| `ui` | none (text only) | `a2ui`: the agent can show forms, choices and cards as A2UI surfaces with the `ui_render` tool (below, "Agent-made UI") |
| `notify` | none | `{"url": ...}`: the webhook told when a task of the agent ends, unless the task names one (needs `AGENTKIT_WEBHOOK_SECRET`) |
| `can_message` | none | names of agents this one may message (below, "Agents messaging agents"): it gets `agents_list`, `agent_message` and `thread_read`. Each named agent may use only toolsets (and runtime toolsets) this one has, so a key that may run this agent reaches no other toolset through it. Not for an agent on a runner (`AGENTKIT_RUNNER_URL`) yet |
| `triggers` | none | triggers that start tasks of the agent without a person, in the default tenant: `[{"id": ..., "prompt": ..., "schedule": "CRON_TZ=Europe/Paris 0 9 * * 1-5"}]`, or `"webhook": true` instead of a schedule (needs `AGENTKIT_WEBHOOK_SECRET`). Stored at startup; the API pauses and resumes them and changes nothing else (below, "Triggers") |
| `memory` | none (no memory tool, nothing stored) | the agent's own notes across conversations: one tool, `memory`, with the commands of Claude's memory tool on files under `/memories` and `grep` (a regular expression over them, `path:line: text`). Files hold plain readable text. Each new message starts with the memory: every file's text while it is at most `store.load_bytes` (default 4 KB), past that the file list, and the agent greps and views what it needs; `load_bytes: -1` loads nothing. `store.backend` is `memory` (in the process, lost on restart), `sqlite` with `store.path` (a file; only in the local image, the others refuse it at startup) or `postgres` (the store's database, `$AGENTKIT_STORE`); `store.scope` is `tenant` (default) or `user` (each user of a tenant apart: needs API keys with a `user`); `store.max_file_bytes`, `store.max_scope_bytes`, `store.max_files` default to 16 KiB, 1 MiB and 200. `native_tool: true` sends it to Claude as the native memory tool type. `index` adds a search store (recall over many items): the tools `recall_search` (words, `"exact phrases"`, a metadata filter), `recall_get` and `recall_save`; `index.backend` is `memory` or `sqlite` with `index.path` (it may be the store's file), `index.scope` as the store's, `index.max_items` and `index.max_text_bytes` default to 10,000 and 32 KiB. `index.auto_recall: true` adds the top `index.k` (default 5) results for each new message to that message as a `<recalled source="memory">` block marked as data, never to the system prompt. `index.embedder` adds search by meaning: `{"type": "voyage", "model": "voyage-3.5-lite"}` (key in `VOYAGE_API_KEY`) or `{"type": "openai", "model": "text-embedding-3-small"}` (`OPENAI_API_KEY`; with `base_url`, any OpenAI-compatible server, such as a local one, and `dimensions` for a model it does not know); `api_key_env` names another variable. `index.modes` lists `keyword`, `semantic` and `hybrid` (reciprocal rank fusion of both), the default first: `["hybrid", "keyword", "semantic"]` with an embedder, `["keyword"]` without. Every item's vector records its model and dimensions: after changing either, semantic and hybrid searches fail until `agentkit memory reembed -config file` embeds the items again (nothing re-embeds on its own). A section may have a store, an index or both. An agent on a runner component reaches its memory at the agent API (`AGENTKIT_MEMORY_URL`), loading included; auto recall is not available there yet |

`agent_api`:

| Field | Default | Meaning |
|---|---|---|
| `max_concurrent_runs` | 16 | past it: 503 with `Retry-After` |
| `run_timeout` | `10m` | per run |
| `max_runs`, `max_conversations`, `retention` | 1000, 1000, `24h` | recent runs whose events are kept in memory to stream; older ones are read from the store |
| `max_body_bytes` | 1 MiB | limit on a run's request body |
| `max_background_runs` | 10 | a tenant's tasks going on at once; past it: 429 `too_many_background_runs` with `Retry-After` |
| `task_time_limit` | `2h` | a task still going on after it fails with `wall_clock_limit` (`0s`: none); `run_timeout` is for chats |
| `messaging.max_hops`, `messaging.max_tokens`, `messaging.max_duration` | 8, 500000, `1h` | a thread of messages between agents: the most messages, the tokens of the runs it starts, and how long it may go on; a send past one stops the thread, with an error to the sender and a notice to the others |
| `messaging.send_every`, `messaging.send_burst` | `2s`, 10 | each agent's sends, per tenant: a burst, then one every `send_every`; past it the send fails with `rate_limited` |

`credentials` (where connectors' stored credentials go): the vault keeps only sealed blobs, AES-256-GCM under `AGENTKIT_CREDENTIALS_KEY`, never plaintext.

| `backend` | `path` | For |
|---|---|---|
| `memory` (default) | none | tests; lost on restart |
| `file` | the file, required | one process: the whole store in one JSON file, created `0600` (a file others may read is refused), written atomically, locked so a second process fails to open it |
| `sqlite` | the SQLite file; default the store's (`AGENTKIT_STORE=sqlite:...`) | one machine, local image only: a `credential_blobs` table beside the store's |
| `postgres` | none: `AGENTKIT_STORE` must be `postgres://` | replicas, dev and cloud images: a `credential_blobs` table in the store's database, migrated with it, under its row-level security |

A toolset uses a stored credential when its spec names a slot: `{"name": "crm", "type": "...", "credential": {"slot": "salesforce", "scope": "tenant"}}`. `scope` is `tenant` (default: the tenant's shared connection) or `user` (each caller's own, so API keys need a `user`). Each call picks the caller's credential at that moment; with none (or one that needs reconnecting), the tool answers "crm is not connected for this account" and the run carries on. A spec naming a slot with no vault (no `AGENTKIT_CREDENTIALS_KEY`) fails startup, or is skipped if `optional`. OAuth2 tokens are refreshed a minute before they expire, once per account across concurrent calls, and written back with compare-and-swap, so replicas on one `postgres` vault share one token chain; a refresh the provider refuses (`invalid_grant`) marks the credential `needs_reconnect`. Refreshing needs the slot's OAuth2 client:

```json
"credentials": {"backend": "postgres", "oauth2": [
  {"slot": "gmail", "token_url": "https://oauth2.googleapis.com/token", "client_id": "123.apps.googleusercontent.com",
   "client_secret_env": "GMAIL_CLIENT_SECRET", "auth_style": "header"}
]}
```

The client secret comes from the variable `client_secret_env` names, never the file; `auth_style` is `header` (default) or `params`.

Putting credentials in (agent API, [openapi.yaml](api/openapi.yaml)):

- `GET /v1/credentials` lists every slot the server's toolsets use, as the caller sees it: the tenant's shared ones and the caller's own. Each entry has its scope, status (`connected`, `not_connected`, `needs_reconnect`), kind and version, never a value. `GET /v1/toolsets` shows the caller's status too.
- `PUT /v1/credentials/{slot}` stores an `api_key`, `basic` or `client_credentials` credential (`{"kind": "api_key", "api_key": "..."}`), and `DELETE` removes one. Add `scope` (`?scope=` for `DELETE`) when a slot is used with both scopes.
- A tenant-scope slot needs the API key's scope `credentials:admin`. A user-scope slot holds the caller's own, so the key needs a `user`.
- A `client_credentials` `token_url` must be `https` on a public address unless its host is in `allow_hosts`; every stored token URL is called through the same guard.

Connecting an account with OAuth2 (authorization code + PKCE) needs the slot's app to have `auth_url` and `redirect_url`:

```json
"credentials": {"backend": "postgres",
  "return_urls": ["https://app.example.com/settings/connections"],
  "oauth2": [{"slot": "gmail", "profile": "google", "client_id": "123.apps.googleusercontent.com",
    "client_secret_env": "GMAIL_CLIENT_SECRET", "scopes": ["https://www.googleapis.com/auth/gmail.readonly"],
    "redirect_url": "https://agents.example.com/oauth/callback"}]}
```

- `profile` (`google`, `hubspot`, `microsoft`, `salesforce`) fills in what the provider needs: the scope or `auth_params` that get a refresh token, Google's and HubSpot's URLs (Microsoft's and Salesforce's depend on the account, so set `auth_url` and `token_url`), and HubSpot's client in the token request's form (`auth_style: params`). Fields you set win. The profiles come from the MCP client, so the `api` image has none.
- `redirect_url` is this server's `/oauth/callback`, registered with the provider.
- `POST /v1/credentials/{slot}/connect` (`{"return_url": "..."}`, optional) answers `{"authorize_url", "expires_at"}`: send the browser there. Call it from that same browser (the console's `fetch` does), on the host of `redirect_url`: the answer sets an HttpOnly, SameSite=Lax cookie that binds the flow to the browser. With an `https` `redirect_url` it is a Secure `__Host-` cookie (Path=/), so the browser must reach the API over https too; with an `http` one (local use) it is limited to `/oauth/callback`. A script that prints the URL for someone to open elsewhere cannot finish the flow.
- The provider sends the browser back to `GET /oauth/callback`, which needs no API key. The `state` says whose tokens they are: 256 bits, single use, ten minutes, kept sealed in the vault so any replica can finish the flow. A browser without the flow's cookie (someone else's link) is refused with a page, and nothing is stored. Each caller has at most 16 flows pending; a 17th drops their own oldest.
- The callback shows a short page, or redirects to the return URL with `result` (`connected`, `denied`, `expired`, `failed`) and `slot`. A return URL must match one of `return_urls` in scheme, host and path.

Without the API, `agentkit credentials put -config agentkit.json -tenant acme [-user u1] -kind api_key crm` writes to the configured vault directly. The value is the first line of stdin. `-kind` is `api_key`, `basic` (with `-username`), `client_credentials` (with `-client-id` and `-token-url`) or `oauth2` (a refresh token).
`runners` places runs in runner pools, each with its own limit, so that background work never takes the slots chats need. Without it there is one pool, `default`, with no limit of its own:

```json
"runners": {
  "pools": [{"name": "chat", "limit": 16}, {"name": "background", "limit": 4}],
  "routes": [{"pool": "chat", "runs": "chat"}],
  "default": "background"
}
```

| Field | Default | Meaning |
|---|---|---|
| `pools[].name` | required | `^[a-zA-Z0-9_-]{1,64}$`, unique; shown as a run's `pool` (`GET /v1/runs/{id}`) and in the logs |
| `pools[].isolation` | `process` | only `process` until the sandbox (T106) and VM (T107) pools; anything else fails startup |
| `pools[].limit` | none | runs going on in the pool at once. A chat past it gets `429 over_capacity` with `Retry-After`; a background run waits in the store until the pool has a free slot |
| `routes[]` | none | tried in order: `pool`, and the runs it takes: `runs` (`chat` or `background`; both when absent), `agents` (names), `isolation` (agents that need at least that much). A route whose pool is less isolated than the agent needs is passed over |
| `default` | the only pool | the pool of the runs no route takes |

A pool that background runs can be routed to (the default, and any route that is not `chat` only) gets a dispatcher, which claims the pool's queued runs (its lane of the run queue) only while the pool has a free slot.

The one-agent form still works: an `agent` section (instead of `agents` and `agent_api`) with the fields of both tables except `name` is served as the agent `default`.

Environment: `AGENTKIT_COMPONENTS` (any of `agent`, `web`, `mcp-server`, comma-separated; `web` serves the console at `/`, a chat client of `agent` on the same address), `AGENTKIT_ADDR` (default `127.0.0.1:8080`), `AGENTKIT_API_TOKEN`, `AGENTKIT_API_KEYS_FILE` and `AGENTKIT_USERS_FILE` (credentials, below; one of them, or `auth` in the config with keys, `jwt` or `users`, is required off loopback), `AGENTKIT_CONFIG`, `AGENTKIT_LOG_LEVEL`, `AGENTKIT_SHUTDOWN_TIMEOUT`, `AGENTKIT_CREDENTIALS_KEY` (the credential vault's key ring, `<id>:<base64 of 32 random bytes>[,<id>:<base64>...]` with the first key sealing, e.g. `k1:$(openssl rand -base64 32)`; required by every `credentials.backend` but `memory`, and a malformed one fails startup). The API's reference: [docs/api/openapi.yaml](api/openapi.yaml) (OpenAPI 3.1); the event format and error codes are also in [tasks/T036](../tasks/T036-agent-api-v0.md) Notes.

### Choosing the model

An agent runs `model` unless a run names another it allows. A model is named `<provider>/<id>` (`anthropic/claude-opus-5-5`, `openai/gpt-5-mini`); a bare id is `anthropic`'s.

```json
"models": {
  "allow": ["anthropic/claude-sonnet-5-5", "openai/gpt-5-mini"],
  "allow_patterns": ["anthropic/claude-opus-.*"],
  "variants": [
    {"match": "anthropic/claude-sonnet-5-5", "system_append": "Keep answers short."},
    {"match": "re:openai/.*", "system": "You are a careful assistant. Use the tools.", "effort": "low"}
  ]
}
```

- `allow` names models exactly; `allow_patterns` are RE2 patterns matched against the whole name (at most 16, 256 bytes each). `model` is the default and always allowed.
- `variants` change the agent on some models; the first whose `match` (a name, or a pattern after `re:`) fits applies. `system` replaces the prompt, `system_append` adds to it; `effort` and `max_tokens` replace the model's. `tool_descriptions` go over the agent's own for those models.
- A run's `model` that the agent does not allow is `400 model_not_allowed`. Without one, a run continues on its conversation's last model, or the agent's default. Switching to another provider in a conversation is `409 model_provider_mismatch` (a provider cannot read another's thinking blocks): start a new conversation. `GET /v1/runs/{id}` shows each run's `model`, and a resume keeps it.
- `GET /v1/agents/{name}` lists the agent's `models` (`choices` are what a picker offers: the names, then the known models a pattern allows); `GET /v1/models` lists the providers this server builds and the models it knows of each.
- Each model the agent allows by name is built at startup (or when a stored agent is saved), so a bad one fails there; a model only a pattern allows is built on its first run. A model of another provider than `model` uses that provider's defaults (`$OPENAI_BASE_URL` and `OPENAI_API_KEY` for `openai`).

### Tool policies

A tool policy decides each tool call by the tool's kind (`read`, `write`, `delete`, `send`, `execute`), risk (`low`, `medium`, `high`), tags, name and toolset: `allow`, `flag` (it runs, and the run's events say so), `approve` (a person must approve it: an agent whose `interaction` is `interactive` or `supervised` suspends the run until someone answers, see below; an `autonomous` one refuses the call, saying so) or `deny` (the model is told "not allowed in this run: ..." and carries on). A tool that declares no kind or risk counts as `write`/`medium`.

Give a preset's name, or rules (the first that matches decides; every field of `match` that is set must match; `tools` and `toolsets` are globs):

```json
"tool_policy": {"rules": [
  {"match": {"toolsets": ["github"], "kinds": ["delete"]}, "action": "deny", "reason": "no deletes on GitHub"},
  {"match": {"tags": ["billing"]}, "action": "flag"},
  {"match": {"kinds": ["read"]}, "action": "allow"}
], "default": "approve"}
```

| Preset | read | write low / medium / high | delete, send, execute |
|---|---|---|---|
| `read_only` | allow | deny | deny |
| `low_risk_writes` | allow | allow / approve / deny | deny |
| `approve_writes` | allow | approve | approve |
| `all` (the default) | allow | allow | allow |

A tool that declares `requires_approval` needs approval under every preset; a rule naming the tool exactly (no wildcard) overrides that.

Policies only narrow. An agent's `tool_policy` is narrowed by the environment's (`"environment": {"tool_policy": "read_only"}` at the top of the config, for every agent) and by the run's (`{"message": "...", "tool_policy": "read_only"}` in `POST /v1/runs`, kept when the run is resumed): each call gets the strictest of their decisions. Every decision other than a plain allow is in the run's events (`tool_policy`, and `tool_flagged` for a flag), with the policy, rule and reason.

#### Questions and approvals

An agent with `"interaction": "interactive"` can wait for people. A tool can ask a question (the `user` toolset's `ask_user`, added with `{"name": "user", "type": "user"}`), and the policy can hold a call with `approve`. The run's events then show an `interrupt` for each waiting call. Once the turn's other calls are done, the run is `suspended` until every interrupt has an answer. The run keeps its pending turn in the conversation's log, so a restart loses nothing. Answer with `POST /v1/runs/{id}/input`:

```sh
curl -s localhost:8080/v1/runs/$RUN/input -H "Authorization: Bearer $AGENTKIT_API_TOKEN" \
  -d '{"answers": [{"id": "toolu_01", "approved": true}, {"id": "toolu_02", "text": "blue"}]}'
```

An approved call runs with exactly the input it was approved with. A denied call's result tells the model who denied it and why (`"approved": false, "reason": "..."`). Answers may come one at a time, and sending the same answer again is safe. A different answer to an interrupt that already has one gets 409. `agentkit chat` shows each interrupt; the next line answers it (`y`, or `n` and a reason, for an approval). A run may narrow the mode (`"interaction": "autonomous"` in `POST /v1/runs`): an autonomous run never waits, is not offered `ask_user`, and refuses a held call, saying why.

### Credentials

`/v1` and `/mcp` need a credential (a bearer token, or for `/v1` a console session) whenever one is configured, and always off loopback (on loopback with none, the server logs a warning and stays open). A tunnel or reverse proxy on the same machine (cloudflared, ngrok, nginx) connects over loopback, so it would publish that open server: set `AGENTKIT_REQUIRE_AUTH=1` (`true` and `yes` work too; or `"auth": {"required": true}`) and serve refuses to start with no credentials whatever the address, and the runner and toolsets components without `AGENTKIT_RPC_TOKEN`. `0`, `false` or `no` do not turn off the file's `true`. A process that serves only `web` (the console's static files) needs no credentials, so the setting does not apply there; serve logs that. `/healthz`, `/readyz` and `/version` are open (but see Cloudflare Access below).

- `AGENTKIT_API_TOKEN`: one key that may do everything (tenant `default`, every scope, every toolset). At least 16 bytes.
- API keys, in the config's `auth` section or in a file named by `AGENTKIT_API_KEYS_FILE` (same JSON, so the hashes can live in a mounted secret). Only the key's hash is stored:

```json
{"auth": {"api_keys": [
  {"id": "ci", "hash": "sha256:...", "tenant": "acme", "user": "ci-bot",
   "scopes": ["agent:read"], "toolsets": ["demo", "search-*"]}
]}}
```

Make a key and its hash with `key=$(openssl rand -hex 24); printf %s "$key" | shasum -a 256` (put `sha256:` before the hex). `scopes`: `agent:read` for the API's GETs, `agent:run` for everything else, `agent:admin` to add, review, change and remove toolsets at run time (section 8), `*` for all three. `toolsets` are names or globs of `mcp.toolsets`; startup fails on one that matches none. A key sees only its tenant's runs and conversations, and only the toolsets it allows: others answer 404, as if they did not exist, over MCP and in the API. An agent is listed and runnable only for a key that allows all of its toolsets. Missing or wrong credentials are 401, a missing scope 403 `forbidden`. `mcp.stateful` cannot be combined with credentials yet.
- Tokens from an identity provider (Auth0, Okta, Google, Keycloak, ...), in `auth.jwt`, beside the keys or alone. agentkit only verifies them, against the provider's published keys; it never issues tokens:

```json
{"auth": {"jwt": {
  "issuer": "https://id.example.com/", "audience": "https://agents.example.com",
  "tenant_claim": "tenant", "scopes_supported": ["agent:read", "agent:run", "mcp:demo"]
}}}
```

A token needs a valid signature (`RS256` or `ES256` unless `algorithms` says otherwise; never `none` or HMAC), `iss` equal to `issuer`, an `aud` naming `audience` (or an MCP endpoint under it, such as `https://agents.example.com/mcp/demo`), and an `exp`; `exp`, `nbf` and `iat` get a `leeway` (default `60s`). The tenant is the claim `tenant_claim` (default `tenant`), or `tenant` for every token; the user is `sub` (`user_claim`); scopes are `scope` or `scp` (`scopes_claim`); each scope `mcp:<toolset>` grants that toolset (`toolset_scope_prefix`), as do the names in `toolsets_claim` if set. Claims are names, never globs. The keys come from `jwks_url`, or the provider's `/.well-known/openid-configuration`; they are fetched again for an unknown key id at most once a minute (`min_refresh_interval`) and when their cache headers say, at most after `max_cache_age` (`1h`). With `auth.jwt`, the MCP endpoints publish their [protected resource metadata](https://www.rfc-editor.org/rfc/rfc9728) at `/.well-known/oauth-protected-resource/mcp/<toolset>` (and the server's at `/.well-known/oauth-protected-resource`), naming `authorization_servers` (default: the issuer), and a 401 points at it, so an MCP client that supports OAuth (Claude Code) finds the provider itself; `audience` must then be the server's origin.

- Local users, who sign in to the console with a username and password, in `auth.users` or in a file named by `AGENTKIT_USERS_FILE` (same JSON). Only an argon2id hash of the password is stored; make one with `read -rs pw && printf '%s\n' "$pw" | agentkit users hash-password` (passwords are 8 to 1024 bytes):

```json
{"auth": {"users": [
  {"username": "ann", "password_hash": "$argon2id$v=19$m=19456,t=2,p=1$...", "tenant": "acme",
   "roles": ["admin"], "toolsets": ["*"]}
], "trusted_proxies": ["10.0.0.0/8"]}}
```

`roles`: `user` may chat (`agent:run`, `agent:read`), `admin` may also manage toolsets (`agent:admin`) and see agents' system prompts. `toolsets` defaults to all (`*`). Signing in (`POST /v1/auth/login`) sets an `HttpOnly`, `SameSite=Strict` session cookie, which works for `/v1` only (never `/mcp`); a session ends after 12 hours idle or 7 days, or at sign-out. Requests other than GET with a session need the `X-Agentkit-CSRF` header from `GET /v1/auth/me` and a same-origin `Origin`. After 10 failed sign-ins for a username (or 100 from an address) each further try waits, doubling up to 15 minutes (429 with `Retry-After`). Off loopback, serve behind a TLS proxy and list it in `trusted_proxies` (addresses or CIDR prefixes): its `X-Forwarded-For` then names the client, and its `X-Forwarded-Proto: https` makes the cookie `Secure` (`__Host-agentkit_session`); without one the server warns at startup. Users alone are enough off loopback, unless `mcp-server` is a component (it needs a key or `auth.jwt`). `GET /v1/auth/config` (no credentials) says which sign-ins the console should offer.

- Cloudflare Access, in `auth.cloudflare_access` (or `AGENTKIT_CF_ACCESS_TEAM_DOMAIN` and `AGENTKIT_CF_ACCESS_AUD`, which win over the file's), for a server behind a Cloudflare Tunnel with an Access application in front. Every request then needs the `Cf-Access-Jwt-Assertion` header Access adds, valid for the team and the application, or gets 401, before any component sees it; only `GET` or `HEAD` of `/healthz`, `/readyz`, `/oauth/callback` and `/oauth/client-metadata.json`, and `POST /v1/hooks/{id}` (an id of letters, digits, `-` and `_`), pass without one (the paths Access must bypass, see [deploy/cloudflare](../deploy/cloudflare/README.md)). The assertion is also a credential: its `email` is the user, in `tenant` (default `default`) with `scopes` and `toolsets` (default `*`), so the console and `/mcp` work with no other sign-in. A key or token sent beside it wins. Without a valid assertion `/readyz` answers its status and `{"status":"ready"}` or `{"status":"not ready"}` only. The file's section makes Access mandatory (no `team_domain` or `aud` is a startup error); the variables alone don't. Access is checked in processes that run `agent`, `mcp-server` or `web`, never in a runner or toolsets alone (their `/rpc/` beside `agent`, `mcp-server` or `web` passes when `AGENTKIT_RPC_TOKEN` is set).

```json
{"auth": {"cloudflare_access": {
  "team_domain": "myteam.cloudflareaccess.com", "aud": "<the application's AUD tag>",
  "emails": ["me@example.com"]
}}}
```

The assertion must be RS256, signed by a key at `https://<team>.cloudflareaccess.com/cdn-cgi/access/certs` (cached as for `auth.jwt`), with `iss` the team domain, `aud` naming the AUD tag, a valid `exp` and `nbf` (60 s leeway), and an `email` (a service token's has none, and is refused). `emails`, when set, is the only emails let in: a second check on the Access policy. Without it every email the policy admits gets `scopes` (default `*`): serve warns, and with authentication required refuses to start unless `scopes` is set. Requests other than GET, HEAD and OPTIONS from another site are refused (403), since the browser sends Access's cookie by itself.

Tasks: `POST /v1/tasks {"input": ..., "agent"?, "policy"?, "interaction"?, "notify"?: {"url": ...}}` starts a background run and answers `202` at once. A task never waits for a person (`autonomous`, the default) unless it is `"interaction": "supervised"` and its agent may wait: it then suspends on an `ask_user` question or a held call (the console lists it under "Waiting for you", and its user's `GET /v1/events` gets a `run_status` `suspended`), and `POST /v1/runs/{id}/input` answers it and lets it go on in the background. The server owns it: closing a connection never stops it (only `POST /v1/runs/{id}/cancel` does), and after a crash the recovery loop resumes it once its lease runs out (`AGENTKIT_RECOVERY_INTERVAL`, default `30s`, is how often it looks). Follow it with `GET /v1/runs/{id}/events` from any number of clients, resuming with `Last-Event-ID`; list them with `GET /v1/runs?background=true&status=running`. Its end is posted to the webhook when `AGENTKIT_WEBHOOK_SECRET` (at least 32 bytes; each tenant's secret derives from it) is set: a JSON `run.ended` with the run, signed `Agentkit-Signature: t=<unix>,v1=<hex HMAC-SHA256 of "<t>.<body>">` under the tenant's secret (`GET /v1/webhooks/secret`, scope `agent:admin`), with `Agentkit-Webhook-Id` (the run's ID: keep the first) and `Agentkit-Webhook-Attempt`; it is retried with backoff for 24h, and the attempts show in the run's `notify`. Webhooks never reach private or loopback addresses except the hosts in `AGENTKIT_WEBHOOK_ALLOW_HOSTS` (comma-separated). Package `pkg/webhook` has `Verify` for receivers.

Triggers: `POST /v1/triggers {"id": ..., "prompt": ..., "schedule"?: ..., "webhook"?: true, "agent"?, "conversation_id"?}` (scope `agent:admin`; also `GET`, `PATCH`, `DELETE /v1/triggers/{id}`, `POST .../pause` and `.../resume`) starts a task of the agent on its prompt at each time of a cron schedule (five fields or `@hourly`, `@daily`...; `CRON_TZ=<zone> ` first for a time zone, default UTC), or each time its inbound webhook is called. The prompt's `{{trigger}}`, `{{time}}` and `{{date}}` are filled in; each firing runs in a new conversation, or continues `conversation_id`. Every firing is recorded (`GET /v1/triggers/{id}/firings`: time, run, outcome). Across replicas sharing a store each due time fires once; a firing while the trigger's previous run goes on is recorded as `skipped`; after downtime a schedule fires once for all the times it missed, then goes on from now. With `AGENTKIT_WEBHOOK_SECRET` set, a trigger with `"webhook": true` gets a path `/v1/hooks/<random id>` and a secret (`GET /v1/triggers/{id}`; `PATCH {"rotate_webhook": true}` replaces both): POST it a JSON body signed as [Standard Webhooks](https://www.standardwebhooks.com/) (`webhook-id`, `webhook-timestamp` within 5 minutes, `webhook-signature: v1,<base64 HMAC-SHA256 of "<id>.<timestamp>.<body>">`). The body (at most 64 KiB) goes to the run after the prompt as a `<third_party_data>` block marked as data, never as instructions; an unsigned, badly signed, stale, replayed (same `webhook-id`) or oversized delivery is refused and starts nothing.

The store: `AGENTKIT_STORE` is `memory` (the default), `sqlite:/path/agentkit.db`, or `postgres://user:pass@host/db`; anything else fails startup. Postgres is migrated at startup unless `AGENTKIT_STORE_MIGRATE=off` (run `agentkit migrate` first then). Postgres also enforces tenant isolation itself, with row-level security, but only for a role that is not a superuser and lacks `BYPASSRLS`. For that, run `agentkit migrate` as the owner and serve as an application role (`GRANT USAGE ON SCHEMA ...; GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ...`). Then set `AGENTKIT_STORE_ADMIN` to a URL whose role has `BYPASSRLS`, for the loop that resumes every tenant's runs; startup fails without it. `AGENTKIT_RUN_LEASE` (default `30s`, at least `1s`) is how long a run stays claimed by a process that stops renewing it: after a crash, a chat run reads as `interrupted` once its lease runs out, and `POST /v1/runs/{id}/resume` continues it.

Several replicas: run each `agentkit serve` on the same Postgres store with `AGENTKIT_BUS=redis://host:6379/0` (default `memory`: one process). Any replica then streams any run's events (`GET /v1/runs/{id}/events`, live, and from `Last-Event-ID` after a reconnect to another replica), and stop and cancel reach the replica running the run. An event's ID is its place in the run: `12` for a stored event, `12.3` for a text delta after it. `GET /v1/events` is the caller's notification stream (SSE) from any replica: a `run_status` each time one of the caller's runs or tasks changes status, then `conversation_updated`; it is resumable with `Last-Event-ID` for an hour, and says `resync` when the client should refetch its lists.

```bash
# runs that outlive the server
AGENTKIT_STORE=sqlite:bin/agentkit.db AGENTKIT_COMPONENTS=agent,mcp-server bin/agentkit serve -config deploy/local/agent/config.json

# a conversation's runs; stop, resume or cancel one; follow its events (from after event 3)
curl -s 'localhost:8080/v1/runs?conversation_id=conv_...' -H "Authorization: Bearer $AGENTKIT_API_TOKEN"
curl -s -X POST localhost:8080/v1/runs/run_.../stop -H "Authorization: Bearer $AGENTKIT_API_TOKEN"
curl -sN -X POST localhost:8080/v1/runs/run_.../resume -H "Authorization: Bearer $AGENTKIT_API_TOKEN"
curl -sN localhost:8080/v1/runs/run_.../events -H "Authorization: Bearer $AGENTKIT_API_TOKEN" -H 'Last-Event-ID: 3'
```

### Agent-made UI (A2UI)

Agents messaging agents: an agent with `"can_message": ["researcher"]` gets three tools. `agents_list` shows whom it may message; `agent_message {"to": "agent:researcher", "text": ..., "thread"?, "wait"?, "timeout"?}` sends, starting a thread or continuing one (a run that a message started continues that message's thread, so a loop between agents meets the hop limit); `thread_read {"thread"}` shows a thread it is part of. The message starts a background run of the recipient in a conversation of its own per thread, and the recipient's final answer goes back as the reply: with `"wait": true` the call returns it (or `"status": "timeout"` with the thread, after `timeout` seconds, default 120), otherwise the call returns at once and the reply later starts a new turn of the sender, labelled with who sent it. A message reaches its recipient as data from another agent (a fixed label, then the message as one JSON line), never as the system prompt or the user's words. A recipient that is busy gets the thread's messages in order once its run ends; a message is processed once, even after a crash. Addresses are `agent:<name>`; `a2a:` ones are refused until E13. `GET /v1/threads` lists the tenant's threads (newest first) and `GET /v1/threads/{id}` shows one with its messages, their states and the runs that processed them.

An agent with `"ui": "a2ui"` gets the `ui_render` tool: its input is a list of [A2UI](https://a2ui.org/) v0.9 messages (`createSurface`, `updateComponents`, `updateDataModel`, `deleteSurface`) drawn with the basic catalog, without Slider, Tabs, Modal, Video and AudioPlayer. The server checks each call (unknown components or properties, a child that is not defined, cycles, sizes, URLs that are not `http(s)`); a bad one is a tool error naming each problem, which the model fixes and sends again. Each message of a good call is a `ui` event on the run's stream, after the call's `tool_result`:

```json
{"v":1,"type":"ui","id":"7","seq":7,"run_id":"run_...","at":"...","turn":1,"call":0,"tool_use_id":"toolu_...","surface_id":"pick_plan","message":{"version":"v0.9","createSurface":{"surfaceId":"pick_plan","catalogId":"https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json"}}}
```

What the person does on a surface (a button's action, with its values) goes back as `ui_action` in place of `message`, to the same conversation; the agent reads it as the next user message:

```bash
curl -sN localhost:8080/v1/runs -H "Authorization: Bearer $AGENTKIT_API_TOKEN" -d '{"conversation_id": "conv_...",
  "ui_action": {"surface_id": "pick_plan", "component_id": "ok", "action": "choose_plan", "context": {"plan": ["pro"]}}}'
```

## 6. Web search

`deploy/local/agent/search.json` adds three web search toolsets to the demo tools, each served over MCP at its own route, and has these agents: `default`, a plain assistant with the demo tools, `search`, a research assistant with the web search toolsets, and `workspace` (section 7):

- `tavily`: `tavily_search` (web search) and `tavily_extract` (a page's content), at `/mcp/tavily`. No key needed.
- `exa`: `exa_search` and `exa_extract` through [Exa](https://exa.ai), at `/mcp/exa`. Each user connects their own Exa key.
- `parallel`: `parallel_search` and `parallel_extract` through [Parallel](https://parallel.ai), at `/mcp/parallel`. Each user connects their own Parallel key.

```sh
scripts/try-agent.sh --agent search     # uses search.json; AGENTKIT_CONFIG picks another
```

Exa and Parallel have no server key in `search.json`: their specs name a credential slot of scope `user` (`"credential": {"slot": "exa", "scope": "user"}`), so each signed-in user stores their own key in the credential vault (section 5, `credentials`), from the console's Connections page or the API, and every call uses the key of the user it runs for:

```sh
curl -X PUT localhost:8080/v1/credentials/exa -H "Authorization: Bearer $MY_KEY" -d '{"kind": "api_key", "api_key": "..."}'
curl -X PUT localhost:8080/v1/credentials/parallel -H "Authorization: Bearer $MY_KEY" -d '{"kind": "api_key", "api_key": "..."}'
```

The caller must be a user: a local user signed in to the console, or an API key with a `user` (`AGENTKIT_API_TOKEN` has none). Until a user connects a key, that user's calls answer "exa is not connected for this account" and the agent carries on with the other tools; `GET /v1/credentials` and `GET /v1/toolsets` show each slot's status for the caller. The slots need a vault, so a server without `AGENTKIT_CREDENTIALS_KEY` skips both toolsets (they are optional); `scripts/try-agent.sh` makes a key for its run (memory backend: keys are lost when it stops). In the split deployment, the agent API and the toolsets component open the same vault (`credentials.backend` `postgres`, the same `AGENTKIT_CREDENTIALS_KEY`), and the calling user reaches the toolsets component with each call. A single-user setup can still read one server key from the environment instead: drop `credential` and set `EXA_API_KEY` or `PARALLEL_API_KEY` (or name another variable in `api_key_env`; a spec cannot have both).

Then ask the `search` agent something current, e.g. `{"message":"What is the latest Go release? Cite the source.","agent":"search"}`: the run's events show a `tool_call` to one of the search tools, and the answer cites URLs. `GET /v1/toolsets` shows which search toolsets are ready, and for a skipped one the reason and the key variable it reads (never the key).

`/mcp` takes the same credentials as the agent API: `AGENTKIT_API_TOKEN`, or the API keys of section 5's `auth` (a key limited to some toolsets sees only those).

### Tavily

No Tavily key is needed: without one the toolset runs keyless, at Tavily's free rate limits. For higher limits set `TAVILY_API_KEY` in the server's environment (`"auth": "auto"` uses it when set; the startup log says which mode is in use). Settings and limits: [connectors/tavily](../connectors/tavily/doc.go).

### Exa

Exa needs an API key (dashboard.exa.ai): each user's own, from a credential slot (above), or one server key from the environment, whose variable the config names (never the key itself). Without a slot and without `EXA_API_KEY` the server still starts: the `exa` toolset is optional, so it is skipped, the log says which variable is missing, and `/readyz` reports `degraded`. With `--docker`, pass the key into the container (`-e EXA_API_KEY`).

Settings of an `exa` toolset (all optional; unknown fields, including a literal `api_key`, fail startup):

| Field | Default | Meaning |
|---|---|---|
| `api_key_env` | `EXA_API_KEY` | environment variable holding the key, for a spec without a credential slot |
| `base_url` | `https://api.exa.ai` | another server, e.g. a local replay (`https`, or `http` to loopback only) |
| `timeout` | `30s` | per call, including one retry on 429 |
| `max_content_chars` | `{"search": 4000, "extract": 20000}` | cap on each result's content; a cut ends with e.g. `…[truncated 12,400 characters; call exa_extract on this URL for the full page]` |
| `search_defaults.type` | `auto` | `instant`, `fast`, `auto`, `deep-lite`, `deep`, `deep-reasoning` |
| `search_defaults.model_chooses_type` | `false` | let the model pick `type` per call |
| `search_defaults.contents` | `highlights` | `highlights` (relevant passages) or `text` (page text) |
| `search_defaults.max_characters` | Exa's default (highlights), the content cap (text) | sent as `maxCharacters` |
| `search_defaults.category` | none | `company`, `publication`, `news`, `personal site`, `financial report`, `people` |
| `extract_defaults.max_characters` | the extract cap | page text fetched per URL |
| `extract_defaults.max_age_hours` | Exa's default | freshness: `0` always fetches, `-1` always uses the cache |

### Parallel

Parallel needs an API key (platform.parallel.ai): each user's own, from a credential slot (above), or one server key from the environment, whose variable the config names (never the key itself). Without a slot and without `PARALLEL_API_KEY` the server still starts: the `parallel` toolset is optional, so it is skipped, and `/readyz` reports `degraded` and names the variable. With the container (section 4), pass the key with `-e PARALLEL_API_KEY` (`scripts/try-agent.sh --docker` passes it when set).

Settings of a `parallel` toolset (all optional; unknown fields, including a literal `api_key`, fail startup; full reference: [connectors/parallel](../connectors/parallel/doc.go)):

| Field | Default | Meaning |
|---|---|---|
| `api_key_env` | `PARALLEL_API_KEY` | environment variable holding the key, for a spec without a credential slot |
| `base_url` | `https://api.parallel.ai` | another server, e.g. a local replay (`https`, or `http` to loopback only) |
| `timeout` | `30s` | per request attempt (one retry on 429) |
| `max_content_chars` | `{"search": 4000, "extract": 20000}` | cap on each result's content; a cut ends with e.g. `…[truncated 12,400 characters; call parallel_extract on this URL for more of the page]` |
| `search_defaults.mode` | `fast` | `turbo`, `fast`, `basic`, `advanced` |
| `search_defaults.allow_model_mode` | `false` | let the model pick `mode` per call |
| `search_defaults.max_chars_per_result` | Parallel's default | excerpt length per result |
| `search_defaults.location` | none | two-letter country code, e.g. `us`, passed to Parallel |
| `search_defaults.fetch_policy` | Parallel's default | `max_age_seconds` (at least 600), `timeout_seconds`, `disable_cache_fallback` |
| `extract_defaults.full_content` | `false` | return each page's full content instead of the excerpts relevant to the objective |
| `extract_defaults.fetch_policy` | Parallel's default | as for search |

### Gmail

The `gmail` toolset (`gmail_search_messages`, `gmail_read_message`, `gmail_read_thread`, `gmail_list_labels`, `gmail_modify_labels`, `gmail_create_draft`, and with `"allow_send": true` the send tools) acts on one Google account. It is not in `search.json`: it needs an OAuth access token, which per-user sign-in (T096) will supply. Until then, for your own account, put a token with the Gmail scopes (`gmail.readonly`, `gmail.compose`, `gmail.modify`) in `GOOGLE_ACCESS_TOKEN`; it expires in about an hour. Add `{"name": "gmail", "type": "gmail", "optional": true}` to a config's toolsets. Without the variable the toolset is skipped. Sending is off unless the settings turn it on, and a send tool asks for approval. Settings and limits: [connectors/google/gmail](../connectors/google/gmail/doc.go).

The `drive` toolset (`drive_search_files`, `drive_get_file`, `drive_read_file`, `drive_list_folder`; with `"read_only": false` also `drive_create_file`, `drive_update_file` and `drive_trash_file`; with `"allow_share": true` as well, `drive_share_file`) acts on one Google account's Drive. It is not in `search.json`: it needs an OAuth access token, which per-user sign-in (T096) will supply. Until then put a token with the Drive scope (`drive.readonly` for reading, `drive` for the write tools) in `GOOGLE_ACCESS_TOKEN`; it expires in about an hour. Add `{"name": "drive", "type": "drive", "optional": true}` to a config's toolsets. Without the variable the toolset is skipped. Trashing and sharing ask for approval, nothing deletes permanently, and nothing shares with "anyone with the link". Settings and limits: [connectors/google/drive](../connectors/google/drive/doc.go).

The `sheets` toolset (`sheets_get_spreadsheet`, `sheets_read_range`; with `"read_only": false` also `sheets_append_rows`, `sheets_update_range` and `sheets_create_spreadsheet`) reads and writes Google Sheets cells by A1 range (`Budget!A1:D20`, or a tab name alone for the whole tab). A read stops at `max_cells` (2,000 by default) on a whole row and names the range to read next. Writes are parsed as if typed (`USER_ENTERED`), so `=SUM(B2:B9)` becomes a formula. Like Drive it needs a token with the Sheets scope (`spreadsheets.readonly`, or `spreadsheets` for writes) in `GOOGLE_ACCESS_TOKEN`, or a per-user credential slot (`"credential": {"slot": "sheets", "scope": "user"}`). Add `{"name": "sheets", "type": "sheets", "optional": true}` to a config's toolsets. Settings and limits: [connectors/google/sheets](../connectors/google/sheets/doc.go).

### HubSpot

The `hubspot` toolset (`hubspot_search`, `hubspot_get_record`; with `"read_only": false` also `hubspot_create_record`, `hubspot_update_record` and `hubspot_create_note`) reads and writes a HubSpot account's contacts, companies, deals and tickets through the CRM API v3. Nothing deletes. Each user connects their own account: give the toolset a slot, `{"name": "hubspot", "type": "hubspot", "optional": true, "credential": {"slot": "hubspot", "scope": "user"}}`, and register a HubSpot public app in `credentials.oauth2` with `"profile": "hubspot"` and the scopes `hubspot.Scopes` lists (`oauth`, `tickets`, `crm.objects.<contacts|companies|deals>.read`, and `.write` with writes on). For development without a vault, put a private app's token in `HUBSPOT_ACCESS_TOKEN` and leave the slot out. Property values are cut at `max_value_chars` (4,000). Settings and limits: [connectors/hubspot](../connectors/hubspot/doc.go).

### Salesforce

The `salesforce` toolset (`salesforce_query` for SOQL SELECTs, `salesforce_search` for text across objects, `salesforce_get_record`, `salesforce_describe_object`; with `"read_only": false` also `salesforce_create_record` and `salesforce_update_record`) works on one user's org through the REST API. Nothing deletes. Each user connects their own org: give the toolset a slot, `{"name": "salesforce", "type": "salesforce", "optional": true, "credential": {"slot": "salesforce", "scope": "user"}}`, and register a connected app in `credentials.oauth2` with `"profile": "salesforce"`, `"scopes": ["api"]` and the login host's `auth_url` and `token_url` (`https://login.salesforce.com/services/oauth2/authorize` and `.../token`). The profile keeps the token's `instance_url`, so each call goes to that user's org, which must be on a Salesforce domain. For development without a vault, put a token in `SALESFORCE_ACCESS_TOKEN` and the org in the `instance_url` setting. A query returns at most `max_records` (200) records a page and names the next page. Settings and limits: [connectors/salesforce](../connectors/salesforce/doc.go).

## 7. Core tools: math and files

Two built-in toolset types need no account (`pkg/coretools`):

- `math`: `add` and `multiply` (a list of numbers), `subtract`, `divide`, `modulo`, `power`, `sqrt`, `round`, and `statistics` (count, sum, min, max, mean, median, standard deviation). Numbers are float64; results are rounded to 15 significant digits, so 0.1 + 0.2 is 0.3. Dividing by zero and overflows are errors the model reads.
- `files`: `list_files`, `read_file` (a range of lines for large files), `search_files` (a regular expression, optionally filtered by a name glob) and `edit_file`, on the files under one `root` folder. Paths are relative to the root, and nothing outside it is reachable, by `..` or through a symlink. `edit_file` replaces `old_text`, which must appear exactly once unless `replace_all` is set, with `new_text`; an empty `old_text` creates a new file and its folders. Writes replace the file atomically and keep its mode. `.git` is never listed or searched.

```json
{"name": "math", "type": "math"},
{"name": "files", "type": "files", "settings": {"root": "/srv/workspace"}},
{"name": "docs", "type": "files", "settings": {"root": "/srv/docs"}, "exclude": ["edit_file"]}
```

`files` settings: `root` (required; relative paths are from the server's working directory), `create_root` (create it when missing; default false, so a typo fails startup), and `max_file_bytes` (default 1 MiB, at most 64 MiB: the largest file read, searched or edited). Excluding `edit_file`, as `docs` does above, makes a read-only toolset.

Any toolset entry can also replace tool descriptions with `descriptions`, a map from tool name to its new description: `"descriptions": {"read_file": "Read one file of the docs. Prefer search_files to find where something is."}`. Each name must be a tool the entry keeps. Use it to tune how a model uses a connector without changing its code; `make eval` (evals/README.md) measures the effect.

`search.json` serves both, at `/mcp/math` and `/mcp/files`, with the agent `workspace` on `.data/workspace` (created at startup):

```sh
scripts/try-agent.sh --agent workspace
bin/agentkit ask -agent workspace "Create notes/todo.md with three items, then tell me the average of 4, 8 and 15."
claude mcp add --transport http agentkit-files http://localhost:8080/mcp/files --header "Authorization: Bearer $AGENTKIT_API_TOKEN"   # or use them from Claude Code
```

`edit_file` is a write tool (kind `write`, risk `medium`); the rest are read-only. An agent (or an environment, or a run) with the tool policy `read_only` cannot call it (section 5, "Tool policies"); without one, anyone with the API token can change files under `files`' root: give it a root you are happy for them to change.

### Knowledge bases

A `knowledge` toolset (`pkg/knowledge`) is a knowledge base: a folder of Markdown (`.md`, `.markdown`) and text (`.txt`) documents that the agent searches for passages to answer from. It is read once at startup: every document is cut into passages along its headings (each at most `chunk_bytes`, default 2,000, and labelled with its section, such as "Returns and refunds > Restocking fee") and indexed in memory. The tools are `knowledge_search` (passages for a query, with their source and section to cite), `knowledge_list` (the documents) and `knowledge_read` (a whole document, in parts of 16 KiB). All three are read-only.

```json
{"name": "handbook", "type": "knowledge", "settings": {"root": "knowledge", "description": "Northwind Bikes' customer support handbook"}}
```

Settings: `root` (required; a relative path is from the config file's folder), `description` (what the base holds, added to the tools' descriptions so the model knows when to search it), `k` (passages per search when the model gives none, default 5), `chunk_bytes` (200 to 16,384), and, for search by meaning, `embedder` (as an agent's `memory.index.embedder`: `{"type": "voyage", "model": "voyage-3.5-lite"}`, or `"openai"` for any OpenAI-compatible server) and `modes`. With an embedder, every passage is embedded at startup. Without an embedder the search is by keyword (BM25), which finds the words of the question, not their synonyms. Folders and files whose names start with `.` are skipped; a symlink is never followed.

The base is the deployment's, like a system prompt: every caller searches the same documents, whatever its tenant. To change it, change the folder and restart. Write it as you would the agent's instructions: a model may follow text it reads, though the tools say that passages are reference material, not instructions.

`search.json` has a sample, the made-up Northwind Bikes support handbook in `deploy/local/agent/knowledge`, and the agent `support`, which answers from it:

```sh
scripts/try-agent.sh --agent support
bin/agentkit ask -agent support "My e-bike shows E07. What should I do?"
```

The split deployment (`make k8s-up`, `make smoke`) runs the same handbook on the toolsets component; `make eval EVAL_SCENARIOS='^knowledge-'` measures how Claude uses it.

### Skills

An agent's `skills` names folders of [Agent Skills](https://www.anthropic.com/engineering/equipping-agents-for-the-real-world-with-agent-skills) (`pkg/skill`): each folder under one is a skill, with a `SKILL.md` (YAML front matter `name` and `description`, then instructions) and any reference files. The same folders Claude Code uses work as they are. Only each skill's name and description go into the system prompt; the agent gets `skill_load` (a skill's instructions and its list of files) and `skill_read` (one of its text files) to read the rest when a task needs it. Reads stay inside the skill's folder (no `..`, absolute paths or symlinks out). Scripts in a skill are never run.

```json
{"name": "search", "toolsets": ["tavily"], "skills": [{"path": "skills"}, {"path": "/srv/team-skills", "optional": true}]}
```

A skill that breaks a rule fails startup with its folder and the rule: no front matter, a `name` that is not `^[a-zA-Z0-9_-]{1,64}$`, an empty or over-1,024-character `description`, a `SKILL.md` over 64 KB, another file over 1 MB, more than 200 files, two skills with one name, or a symlink out of the folder. An `optional` folder that fails is skipped and named in `/readyz`, which also lists each agent's skills under `details.skills`. `search.json`'s `search` agent has a demo skill, `research-brief`, in `deploy/local/agent/skills/`.

A skill's front matter may list `tools:` (names or globs, like Claude Code's `allowed-tools`): when the model loads the skill, those of the agent's deferred tools load with it (next section). A listed tool the agent doesn't have loads nothing, and startup logs a warning naming it: a skill never gives an agent a tool it doesn't have.

### Deferred tools

An agent with many tools wastes context, and confuses the model, if every definition goes with every request. `defer_loading` holds tools back until the model needs them:

```json
{"name": "ops", "toolsets": ["github", "linear", "demo"], "defer_loading": ["github", "linear", "calc*"]}
```

Each entry is a toolset name (all its tools) or a glob on tool names; `true` defers every toolset tool. Skill and memory tools are never deferred. An agent with deferred tools gets one more, `tool_search`: the model gives a few keywords (or a tool's name), and the best matches by name, description and tags are loaded for the rest of the conversation, a later run or a resume included (each result records what it loaded, in the transcript). A skill that lists the tools loads them too. With Claude (`model.tool_search: "native"`, the default) every request lists every tool with `defer_loading` and the search result references the found tools, so the tool list never changes and the prompt cache holds; with `"emulated"` (OpenAI-compatible models) a found tool is added to the next request. Loading changes no policy: a loaded tool's calls are checked like any other's. The console shows "Loaded N tools" as a step.

A run can choose too, for itself and its resumes: `"tools": {"toolsets": ["github"], "search": "search"}` in `POST /v1/runs` keeps only the named toolsets of the agent (absent: all; `[]`: none) and holds their tools back behind `tool_search` (`"search"`) or sends them all (`"direct"`, which overrides the agent's `defer_loading`). Tools of no toolset (skills, memory, `ask_user`) are always kept. The console's chat sets it with its Tools button. A name that is not one of the agent's toolsets is 400.

## 8. Other MCP servers' tools

A toolset of type `mcp` is another MCP server's tools, used by agents and served again at `/mcp/<name>` like any connector:

```json
{"name": "linear", "type": "mcp", "optional": true, "settings": {
  "url": "https://mcp.linear.app/mcp",
  "headers": {"Authorization": "Bearer ${LINEAR_API_KEY}"}}}
```

```json
{"name": "fs", "type": "mcp", "settings": {
  "transport": "stdio", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/srv/docs"]}}
```

Settings: `url`; `transport` (`auto`, the default: Streamable HTTP, then the older HTTP+SSE; or `http`, `sse`, `stdio`); `command`, `args` and `env` for stdio (`env` names the variables the command gets; it gets `PATH` and `HOME` and nothing else); `headers` (`${NAME}` is replaced by the variable; a header carrying a secret must use one, and headers go only over https or to this machine); `timeout` per call (default `60s`); `connect_timeout` (default `15s`); `max_result_bytes` (default 1 MiB). `include` and `exclude` pick tools, as for any toolset. A server of the config can't sign in with MCP OAuth; add it at run time with `"auth": "oauth"` (section 8).

The server is contacted at startup: one that does not answer fails startup, or with `"optional": true` is skipped and named in `/readyz`. Tools keep the server's names; an agent using two servers that both have a `search` sets `"prefix_tool_names": true` to get `linear__search` and `notion__search`. The server's descriptions are cleaned of hidden characters, its annotations can raise a tool's risk but not lower it, and its results are treated as third-party data.

**Pinning.** A server can change a tool's description after you trusted it (a "rug pull"). Two guards:

- While running, when the server says its tool list changed, every tool whose definition now differs from the one the agent was given is disabled: calls fail with "the MCP server changed this tool's definition", and the log names the tools. New tools appear only after a restart.
- `pins` fixes which definitions are allowed at all, across restarts. Print the current ones with `agentkit mcp-tools <url>` (add `-header "Authorization: Bearer ..."` if the server needs it) and paste the `"pins"` block into the toolset's settings. A tool that is not pinned, or whose definition has changed since, is left out with a warning, so only definitions you have looked at reach the model.

### Adding a server while the server runs

An admin (a key with the scope `agent:admin`, or `AGENTKIT_API_TOKEN`) can add an MCP server without editing the config or restarting. The server is contacted, its tools are listed, and every tool is off until approved. Here the server's config has `"runtime_toolsets": {"header_env": ["LINEAR_API_KEY"]}` and the variable set; the single quotes keep `${LINEAR_API_KEY}` for the server to fill in:

```sh
agentkit toolsets add -name linear -header 'Authorization: Bearer ${LINEAR_API_KEY}' https://mcp.linear.app/mcp
agentkit toolsets review linear           # each tool: new, changed (and how), approved, removed, rejected
agentkit toolsets review -json linear     # the raw definitions, and the approved one next to a changed one
agentkit toolsets approve linear          # every new and changed tool, as the review shows it; or name tools
agentkit toolsets list                    # config toolsets and added ones, with their status
agentkit toolsets disable linear          # or enable, or delete
```

An agent gets the approved tools of the toolsets in its `runtime_toolsets`, as `linear__search`, from its next turn on. A toolset belongs to the tenant that added it: another tenant's agent gets none of its tools. The approved definitions are stored, so the pinning holds across restarts: a tool the server changes or adds is off, and the toolset says `review_needed`, until an admin approves it again. The same calls are in the API (`POST /v1/toolsets`, `GET`/`POST /v1/toolsets/{name}/review`, `PATCH` and `DELETE /v1/toolsets/{name}`; [openapi.yaml](api/openapi.yaml)).

The `runtime_toolsets` section of the config:

| Field | Default | Meaning |
|---|---|---|
| `enabled` | true | false turns the API off |
| `allow_hosts` | none | internal hosts a URL may name (`mcp.internal`, `*.corp.example.com`); they may resolve to private addresses and use http. Every other URL must be https to a public address, checked when connecting |
| `header_env` | none | the variables a header may use as `${NAME}`. Headers carrying a secret must use one, so no secret is stored |
| `max_toolsets` | 50 | per tenant |
| `connect_timeout` | `10s` | connecting and listing tools |
| `registry` | `{"enabled": true}` | the MCP Registry `agentkit toolsets search` and `GET /v1/mcp-registry/search` look in: `url` is the official one unless set (a company's subregistry with the same API); `"enabled": false` turns the search off |
| `mcp_servers.allow` | any | URL patterns admins may add servers at, for every tenant: `mcp.linear.app`, `https://*.example.com/mcp/*`. A URL no pattern matches is refused (`403 url_not_allowed`) before anything connects to it, and so is a redirect to one. Write an international host in its `xn--` form; while a list applies, a URL with `.` or `..` path segments is refused |
| `tenants` | none | settings of a tenant's own, replacing the ones above: `{"acme": {"mcp_servers": {"allow": ["mcp.linear.app"]}}}`; an empty list lets that tenant add any URL |

Finding a server: `agentkit toolsets search linear` lists the servers the MCP Registry has for "linear" that can be added by URL (those with a Streamable HTTP or SSE remote; servers that are only packages to run are left out), with a suggested name and whether `mcp_servers.allow` lets you add each one. A listing is not a review: adding one is probed and its tools stay off until approved, as above. If the registry is down the search says so (`502 registry_unavailable`), and adding by URL still works.

**A server that signs people in with MCP OAuth** (Linear, Notion, GitHub, ...) answers the plain add with `422 auth_required` and `"auth": "oauth"` in the error. Add it with `"auth": "oauth"`, and `"scope": "user"` for each person's own account (or `"tenant"`, the default, for one account the tenant shares). This needs the credential vault (`AGENTKIT_CREDENTIALS_KEY`) and the server's public origin in `AGENTKIT_PUBLIC_URL` (`https://agents.example.com`): the authorization server sends the browser back to `AGENTKIT_PUBLIC_URL/oauth/callback`, and over https `AGENTKIT_PUBLIC_URL/oauth/client-metadata.json` is agentkit's client ID (CIMD). agentkit finds the authorization server when the toolset is added and keeps it (`issuer`); it registers a client per tenant (RFC 7591) when the server takes no client ID document, or uses `"oauth_client": {"client_id": "...", "client_secret": "${NAME}"}`, the secret from a `header_env` variable.

```sh
curl -s -X POST localhost:8080/v1/toolsets -H "Authorization: Bearer $AGENTKIT_API_TOKEN" \
  -d '{"type":"mcp","name":"linear","url":"https://mcp.linear.app/mcp","auth":"oauth","scope":"user"}'
curl -s -X POST localhost:8080/v1/credentials/linear/connect -H "Authorization: Bearer $KEY" -d '{}'
```

The connect call returns `authorize_url`: open it in the browser that made the call (it keeps the flow's cookie), sign in, and the callback stores the token in the vault under the toolset's name, the caller's own for scope `user`. The tools are listed through a connection, so the admin connects their own account, then reviews them as above. Each person then connects theirs on Connections (or with the same call); a person who hasn't gets "not connected" from the tools, never someone else's token. A refresh token the server refuses turns the connection to `needs_reconnect` ("reconnect needed" from the tools) until they connect again. `GET /v1/toolsets` and the toolset's review show the caller's `connection`.

Not yet: agents on a separate runner component.

## 9. Agents defined through the API

An admin (scope `agent:admin`) can define agents without editing the config or restarting. A definition has the shape of an entry of `agents` in the config file, and is checked as serve checks the file (toolsets exist, tool names do not clash, the model builds); a refused one comes back as `400 invalid_definition` with the fields at fault, and saves nothing. Each save is a new version, a draft until published; new runs use the published version, and a run keeps the version it started on (`agent_version` in `GET /v1/runs/{id}`), resumed or not. Agents belong to the tenant that saved them.

```sh
TOKEN=$AGENTKIT_API_TOKEN
curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/v1/agents/writer/versions \
  -d '{"definition": {"system": "You write.", "toolsets": ["demo"]}, "message": "first", "publish": true}'
curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/v1/agents/writer/versions      # every version, oldest first
curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/v1/agents/writer/publish -d '{"version": 1}'   # roll back
curl -s -X DELETE -H "Authorization: Bearer $TOKEN" localhost:8080/v1/agents/writer/versions/3       # a draft only
agentkit export ./backup     # each published stored agent to ./backup/agents/<name>.json
agentkit import ./backup     # save and publish each file on the server in $AGENTKIT_SERVER (-draft: drafts)
```

The agents of the config file are listed with `"read_only": true` and cannot be changed through the API (`409 read_only`). A stored agent may name any toolset of `mcp.toolsets`, so with the registry on the agent API builds them all. Stored agents cannot have `skills` or `memory` yet (T076), nor `ui` or `notify` (a task names its own webhook), and isolation other than `process` waits for sandbox pools. An `openai` model of a stored agent uses `$OPENAI_BASE_URL` and `OPENAI_API_KEY`, or a `base_url` and `api_key_env` an agent of the config file uses: any other would let a tenant admin send any variable of the server to any server.

The `agent_registry` section of the config:

| Field | Default | Meaning |
|---|---|---|
| `enabled` | true | false turns it off. It is off when the agents run on a runner elsewhere (`AGENTKIT_RUNNER_URL`) |
| `cache_size` | 256 | stored agents kept built, across tenants and versions |

## Troubleshooting

- **`startup: AGENTKIT_API_TOKEN ...`**: set a token of at least 16 bytes when listening on a non-loopback address (always the case in Docker).
- **401 from `/mcp/...` in Claude Code**: add the token with `--header "Authorization: Bearer $AGENTKIT_API_TOKEN"` (`claude mcp add`). A 404 for a toolset that exists means the key does not allow it.
- **`run_error` with `model_auth`**: the API key is missing or wrong.
- **Port in use**: `PORT=9090 scripts/try-agent.sh`.
- **Logs**: JSON on stderr; the script writes them to `bin/try-agent.log`. `AGENTKIT_LOG_LEVEL=debug` for more.
