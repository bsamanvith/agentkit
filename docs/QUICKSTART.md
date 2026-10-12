# Quickstart: run the agent server locally

One process serves two things:

- the **agent API**: `POST /v1/runs` sends a prompt to Claude, which our harness runs with tools from the configured toolsets; the reply streams back as Server-Sent Events, and follow-ups continue the conversation;
- the same **toolsets over MCP** at `/mcp/<name>`, for Claude Code, Codex or any MCP client.

Runs and conversations are kept in memory for now (lost on restart); persistence comes with the store and runner (M4).

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

# health, readiness, version (no token)
curl -s localhost:8080/healthz; curl -s localhost:8080/readyz; curl -s localhost:8080/version
```

Use the tools from Claude Code: `claude mcp add --transport http agentkit-demo http://localhost:8080/mcp/demo`.

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

`deploy/local/agent/config.json` is the starting point. The `agent` section (all fields optional; unknown fields fail startup):

| Field | Default | Meaning |
|---|---|---|
| `model.id` | `claude-opus-5-5` | Claude model |
| `model.effort` | adapter default (medium) | `low` … `max` |
| `model.max_tokens` | adapter default | output limit per model call |
| `system` | none | system prompt |
| `toolsets` | none | names from `mcp.toolsets` the agent may use |
| `prefix_tool_names` | false | name tools `<toolset>__<tool>` (needed when two toolsets share a tool name) |
| `max_turns`, `max_total_tokens` | 25, unlimited | budgets per run |
| `max_concurrent_runs` | 16 | past it: 503 with `Retry-After` |
| `run_timeout` | `10m` | per run |

Environment: `AGENTKIT_COMPONENTS` (`agent`, `mcp-server`, or both), `AGENTKIT_ADDR` (default `127.0.0.1:8080`), `AGENTKIT_API_TOKEN` (required off loopback), `AGENTKIT_CONFIG`, `AGENTKIT_LOG_LEVEL`, `AGENTKIT_SHUTDOWN_TIMEOUT`. The full reference, event format and error codes: [tasks/T036](../tasks/T036-agent-api-v0.md) Notes.

## 6. Web search

`deploy/local/agent/search.json` adds three web search toolsets to the demo tools, each served over MCP at its own route and given to the agent:

- `tavily`: `tavily_search` (web search) and `tavily_extract` (a page's content), at `/mcp/tavily`. No key needed.
- `exa`: `exa_search` and `exa_extract` through [Exa](https://exa.ai), at `/mcp/exa`. Needs `EXA_API_KEY`.
- `parallel`: `parallel_search` and `parallel_extract` through [Parallel](https://parallel.ai), at `/mcp/parallel`. Needs `PARALLEL_API_KEY`.

```sh
export EXA_API_KEY=...        # optional: without it Exa is skipped
export PARALLEL_API_KEY=...   # optional: without it Parallel is skipped
AGENTKIT_CONFIG=deploy/local/agent/search.json scripts/try-agent.sh
```

Then ask something current, e.g. `{"message":"What is the latest Go release? Cite the source."}`: the run's events show a `tool_call` to one of the search tools, and the answer cites URLs.

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

Parallel needs an API key (platform.parallel.ai). The config names the environment variable it is read from, never the key itself. Without `PARALLEL_API_KEY` the server still starts: the `parallel` toolset is optional, so it is skipped, and `/readyz` reports `degraded` and names the variable. With the container (section 4), pass the key with `-e PARALLEL_API_KEY`; `scripts/try-agent.sh --docker` does not pass it yet.

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
