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

The demo shows:

1. a streamed run where Claude uses the `calculate` tool (17 × 23 = 391), with its events: `run_start`, `tool_call`, `tool_result`, `turn_done`, `text_delta`, `turn_done`, `run_done`;
2. a follow-up in the same conversation, as JSON (`400`);
3. the same tools listed over MCP at `/mcp/demo` (`calculate`, `current_time`, `echo`);
4. a request without the token refused with 401.

It then prints the token and ready-to-paste commands.

## 3. By hand

```sh
go build -o bin/agentkit ./cmd/agentkit
export AGENTKIT_API_TOKEN=$(openssl rand -hex 16)
AGENTKIT_COMPONENTS=agent,mcp-server bin/agentkit serve -config deploy/local/agent/config.json
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

Use the tools from Claude Code: `claude mcp add --transport http agentkit-demo http://localhost:8080/mcp/demo`.

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
  -e AGENTKIT_COMPONENTS=agent,mcp-server \
  -v "$PWD/deploy/local/agent/config.json:/etc/agentkit/config.json:ro" \
  agentkit:latest
```

Inside the container the server listens on `:8080` (not loopback), so `AGENTKIT_API_TOKEN` is required. `--stop-timeout 20` lets runs drain within the 15 s shutdown deadline.

## 5. Configuration

`deploy/local/agent/config.json` is the starting point: one agent. `deploy/local/agent/search.json` has two (section 6). A server runs one or more agents, each with its own model, system prompt and toolsets, listed in `agents`; the API's limits are in `agent_api`. All fields are optional except `name`; unknown fields fail startup.

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

A run names its agent (`{"message": "...", "agent": "math"}`); without one it gets the agent named `default` (or the only agent). A conversation stays with its agent: continuing it with another is `409 agent_mismatch`.

Each entry of `agents`:

| Field | Default | Meaning |
|---|---|---|
| `name` | required | `^[a-zA-Z0-9_-]{1,64}$`, unique |
| `description` | none | shown in `GET /v1/agents` |
| `model.id` | `claude-haiku-5-5` | Claude model |
| `model.effort` | adapter default (medium) | `low` … `max` |
| `model.max_tokens` | adapter default | output limit per model call |
| `system` | a short generic prompt | system prompt |
| `toolsets` | none | names from `mcp.toolsets` the agent may use |
| `prefix_tool_names` | false | name tools `<toolset>__<tool>` (needed when two toolsets share a tool name) |
| `max_turns`, `max_total_tokens` | 25, unlimited | budgets per run |

`agent_api`:

| Field | Default | Meaning |
|---|---|---|
| `max_concurrent_runs` | 16 | past it: 503 with `Retry-After` |
| `run_timeout` | `10m` | per run |
| `max_runs`, `max_conversations`, `retention` | 1000, 1000, `24h` | recent runs whose events are kept in memory to stream; older ones are read from the store |
| `max_body_bytes` | 1 MiB | limit on a run's request body |

The one-agent form still works: an `agent` section (instead of `agents` and `agent_api`) with the fields of both tables except `name` is served as the agent `default`.

Environment: `AGENTKIT_COMPONENTS` (`agent`, `mcp-server`, or both), `AGENTKIT_ADDR` (default `127.0.0.1:8080`), `AGENTKIT_API_TOKEN` (required off loopback), `AGENTKIT_CONFIG`, `AGENTKIT_LOG_LEVEL`, `AGENTKIT_SHUTDOWN_TIMEOUT`.

The store: `AGENTKIT_STORE` is `memory` (the default), `sqlite:/path/agentkit.db`, or `postgres://user:pass@host/db`; anything else fails startup. Postgres is migrated at startup unless `AGENTKIT_STORE_MIGRATE=off` (run `agentkit migrate` first then). `AGENTKIT_RUN_LEASE` (default `30s`, at least `1s`) is how long a run stays claimed by a process that stops renewing it: after a crash, a chat run reads as `interrupted` once its lease runs out, and `POST /v1/runs/{id}/resume` continues it.

```bash
# runs that outlive the server
AGENTKIT_STORE=sqlite:bin/agentkit.db AGENTKIT_COMPONENTS=agent,mcp-server bin/agentkit serve -config deploy/local/agent/config.json

# a conversation's runs; stop, resume or cancel one; follow its events (from after seq 3)
curl -s 'localhost:8080/v1/runs?conversation_id=conv_...' -H "Authorization: Bearer $AGENTKIT_API_TOKEN"
curl -s -X POST localhost:8080/v1/runs/run_.../stop -H "Authorization: Bearer $AGENTKIT_API_TOKEN"
curl -sN -X POST localhost:8080/v1/runs/run_.../resume -H "Authorization: Bearer $AGENTKIT_API_TOKEN"
curl -sN localhost:8080/v1/runs/run_.../events -H "Authorization: Bearer $AGENTKIT_API_TOKEN" -H 'Last-Event-ID: 3'
``` The API's reference: [docs/api/openapi.yaml](api/openapi.yaml) (OpenAPI 3.1); the event format and error codes are also in [tasks/T036](../tasks/T036-agent-api-v0.md) Notes.

## 6. Web search

`deploy/local/agent/search.json` adds three web search toolsets to the demo tools, each served over MCP at its own route, and has two agents: `default`, a plain assistant with the demo tools, and `search`, a research assistant with the web search toolsets:

- `tavily`: `tavily_search` (web search) and `tavily_extract` (a page's content), at `/mcp/tavily`. No key needed.
- `exa`: `exa_search` and `exa_extract` through [Exa](https://exa.ai), at `/mcp/exa`. Needs `EXA_API_KEY`.
- `parallel`: `parallel_search` and `parallel_extract` through [Parallel](https://parallel.ai), at `/mcp/parallel`. Needs `PARALLEL_API_KEY`.

```sh
export EXA_API_KEY=...        # optional: without it Exa is skipped
export PARALLEL_API_KEY=...   # optional: without it Parallel is skipped
scripts/try-agent.sh --agent search     # uses search.json; AGENTKIT_CONFIG picks another
```

Then ask the `search` agent something current, e.g. `{"message":"What is the latest Go release? Cite the source.","agent":"search"}`: the run's events show a `tool_call` to one of the search tools, and the answer cites URLs. `GET /v1/toolsets` shows which search toolsets are ready, and for a skipped one the reason and the key variable it reads (never the key).

Do not expose a toolset that holds a key on a public address yet: until per-route auth for `/mcp` lands (T040), anyone who can reach the server can spend the key's quota. Keep `AGENTKIT_ADDR` on loopback (the default).

### Tavily

No Tavily key is needed: without one the toolset runs keyless, at Tavily's free rate limits. For higher limits set `TAVILY_API_KEY` in the server's environment (`"auth": "auto"` uses it when set; the startup log says which mode is in use). Settings and limits: [connectors/tavily](../connectors/tavily/doc.go).

### Exa

Exa needs an API key (dashboard.exa.ai); the config names the environment variable it is read from, never the key itself. Without `EXA_API_KEY` the server still starts: the `exa` toolset is optional, so it is skipped, the log says which variable is missing, and `/readyz` reports `degraded`. With `--docker`, pass the key into the container (`-e EXA_API_KEY`).

Settings of an `exa` toolset (all optional; unknown fields, including a literal `api_key`, fail startup):

| Field | Default | Meaning |
|---|---|---|
| `api_key_env` | `EXA_API_KEY` | environment variable holding the key |
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

Parallel needs an API key (platform.parallel.ai). The config names the environment variable it is read from, never the key itself. Without `PARALLEL_API_KEY` the server still starts: the `parallel` toolset is optional, so it is skipped, and `/readyz` reports `degraded` and names the variable. With the container (section 4), pass the key with `-e PARALLEL_API_KEY` (`scripts/try-agent.sh --docker` passes it when set).

Settings of a `parallel` toolset (all optional; unknown fields, including a literal `api_key`, fail startup; full reference: [connectors/parallel](../connectors/parallel/doc.go)):

| Field | Default | Meaning |
|---|---|---|
| `api_key_env` | `PARALLEL_API_KEY` | environment variable holding the key |
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

## Troubleshooting

- **`startup: AGENTKIT_API_TOKEN ...`**: set a token of at least 16 bytes when listening on a non-loopback address (always the case in Docker).
- **`run_error` with `model_auth`**: the API key is missing or wrong.
- **Port in use**: `PORT=9090 scripts/try-agent.sh`.
- **Logs**: JSON on stderr; the script writes them to `bin/try-agent.log`. `AGENTKIT_LOG_LEVEL=debug` for more.
