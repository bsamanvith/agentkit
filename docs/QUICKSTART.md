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

Environment: `AGENTKIT_COMPONENTS` (any of `agent`, `web`, `mcp-server`, comma-separated; `web` serves the console at `/`, a chat client of `agent` on the same address), `AGENTKIT_ADDR` (default `127.0.0.1:8080`), `AGENTKIT_API_TOKEN` and `AGENTKIT_API_KEYS_FILE` (credentials, below; one of them, or `auth` in the config, is required off loopback), `AGENTKIT_CONFIG`, `AGENTKIT_LOG_LEVEL`, `AGENTKIT_SHUTDOWN_TIMEOUT`, `AGENTKIT_CREDENTIALS_KEY` (the credential vault's key ring, `<id>:<base64 of 32 random bytes>[,<id>:<base64>...]` with the first key sealing, e.g. `k1:$(openssl rand -base64 32)`; optional until connectors store credentials, but a malformed one fails startup). The API's reference: [docs/api/openapi.yaml](api/openapi.yaml) (OpenAPI 3.1); the event format and error codes are also in [tasks/T036](../tasks/T036-agent-api-v0.md) Notes.

### Credentials

`/v1` and `/mcp` need a bearer credential whenever one is configured, and always off loopback (on loopback with none, the server logs a warning and stays open). `/healthz`, `/readyz` and `/version` are always open.

- `AGENTKIT_API_TOKEN`: one key that may do everything (tenant `default`, every scope, every toolset). At least 16 bytes.
- API keys, in the config's `auth` section or in a file named by `AGENTKIT_API_KEYS_FILE` (same JSON, so the hashes can live in a mounted secret). Only the key's hash is stored:

```json
{"auth": {"api_keys": [
  {"id": "ci", "hash": "sha256:...", "tenant": "acme", "user": "ci-bot",
   "scopes": ["agent:read"], "toolsets": ["demo", "search-*"]}
]}}
```

Make a key and its hash with `key=$(openssl rand -hex 24); printf %s "$key" | shasum -a 256` (put `sha256:` before the hex). `scopes`: `agent:read` for the API's GETs, `agent:run` for everything else, `*` for both. `toolsets` are names or globs of `mcp.toolsets`; startup fails on one that matches none. A key sees only its tenant's runs and conversations, and only the toolsets it allows: others answer 404, as if they did not exist, over MCP and in the API. An agent is listed and runnable only for a key that allows all of its toolsets. Missing or wrong credentials are 401, a missing scope 403 `forbidden`. `mcp.stateful` cannot be combined with credentials yet.

The store: `AGENTKIT_STORE` is `memory` (the default), `sqlite:/path/agentkit.db`, or `postgres://user:pass@host/db`; anything else fails startup. Postgres is migrated at startup unless `AGENTKIT_STORE_MIGRATE=off` (run `agentkit migrate` first then). Postgres also enforces tenant isolation itself, with row-level security, but only for a role that is not a superuser and lacks `BYPASSRLS`. For that, run `agentkit migrate` as the owner and serve as an application role (`GRANT USAGE ON SCHEMA ...; GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ...`). Then set `AGENTKIT_STORE_ADMIN` to a URL whose role has `BYPASSRLS`, for the loop that resumes every tenant's runs; startup fails without it. `AGENTKIT_RUN_LEASE` (default `30s`, at least `1s`) is how long a run stays claimed by a process that stops renewing it: after a crash, a chat run reads as `interrupted` once its lease runs out, and `POST /v1/runs/{id}/resume` continues it.

Several replicas: run each `agentkit serve` on the same Postgres store with `AGENTKIT_BUS=redis://host:6379/0` (default `memory`: one process). Any replica then streams any run's events (`GET /v1/runs/{id}/events`, live, and from `Last-Event-ID` after a reconnect to another replica), and stop and cancel reach the replica running the run. An event's ID is its place in the run: `12` for a stored event, `12.3` for a text delta after it.

```bash
# runs that outlive the server
AGENTKIT_STORE=sqlite:bin/agentkit.db AGENTKIT_COMPONENTS=agent,mcp-server bin/agentkit serve -config deploy/local/agent/config.json

# a conversation's runs; stop, resume or cancel one; follow its events (from after event 3)
curl -s 'localhost:8080/v1/runs?conversation_id=conv_...' -H "Authorization: Bearer $AGENTKIT_API_TOKEN"
curl -s -X POST localhost:8080/v1/runs/run_.../stop -H "Authorization: Bearer $AGENTKIT_API_TOKEN"
curl -sN -X POST localhost:8080/v1/runs/run_.../resume -H "Authorization: Bearer $AGENTKIT_API_TOKEN"
curl -sN localhost:8080/v1/runs/run_.../events -H "Authorization: Bearer $AGENTKIT_API_TOKEN" -H 'Last-Event-ID: 3'
```

## 6. Web search

`deploy/local/agent/search.json` adds three web search toolsets to the demo tools, each served over MCP at its own route, and has these agents: `default`, a plain assistant with the demo tools, `search`, a research assistant with the web search toolsets, and `workspace` (section 7):

- `tavily`: `tavily_search` (web search) and `tavily_extract` (a page's content), at `/mcp/tavily`. No key needed.
- `exa`: `exa_search` and `exa_extract` through [Exa](https://exa.ai), at `/mcp/exa`. Needs `EXA_API_KEY`.
- `parallel`: `parallel_search` and `parallel_extract` through [Parallel](https://parallel.ai), at `/mcp/parallel`. Needs `PARALLEL_API_KEY`.

```sh
export EXA_API_KEY=...        # optional: without it Exa is skipped
export PARALLEL_API_KEY=...   # optional: without it Parallel is skipped
scripts/try-agent.sh --agent search     # uses search.json; AGENTKIT_CONFIG picks another
```

Then ask the `search` agent something current, e.g. `{"message":"What is the latest Go release? Cite the source.","agent":"search"}`: the run's events show a `tool_call` to one of the search tools, and the answer cites URLs. `GET /v1/toolsets` shows which search toolsets are ready, and for a skipped one the reason and the key variable it reads (never the key).

`/mcp` takes the same credentials as the agent API: `AGENTKIT_API_TOKEN`, or the API keys of section 5's `auth` (a key limited to some toolsets sees only those).

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

### Gmail

The `gmail` toolset (`gmail_search_messages`, `gmail_read_message`, `gmail_read_thread`, `gmail_list_labels`, `gmail_modify_labels`, `gmail_create_draft`, and with `"allow_send": true` the send tools) acts on one Google account. It is not in `search.json`: it needs an OAuth access token, which per-user sign-in (T096) will supply. Until then, for your own account, put a token with the Gmail scopes (`gmail.readonly`, `gmail.compose`, `gmail.modify`) in `GOOGLE_ACCESS_TOKEN`; it expires in about an hour. Add `{"name": "gmail", "type": "gmail", "optional": true}` to a config's toolsets. Without the variable the toolset is skipped. Sending is off unless the settings turn it on, and a send tool asks for approval. Settings and limits: [connectors/google/gmail](../connectors/google/gmail/doc.go).

The `drive` toolset (`drive_search_files`, `drive_get_file`, `drive_read_file`, `drive_list_folder`; with `"read_only": false` also `drive_create_file`, `drive_update_file` and `drive_trash_file`; with `"allow_share": true` as well, `drive_share_file`) acts on one Google account's Drive. It is not in `search.json`: it needs an OAuth access token, which per-user sign-in (T096) will supply. Until then put a token with the Drive scope (`drive.readonly` for reading, `drive` for the write tools) in `GOOGLE_ACCESS_TOKEN`; it expires in about an hour. Add `{"name": "drive", "type": "drive", "optional": true}` to a config's toolsets. Without the variable the toolset is skipped. Trashing and sharing ask for approval, nothing deletes permanently, and nothing shares with "anyone with the link". Settings and limits: [connectors/google/drive](../connectors/google/drive/doc.go).

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

`search.json` serves both, at `/mcp/math` and `/mcp/files`, with the agent `workspace` on `.data/workspace` (created at startup):

```sh
scripts/try-agent.sh --agent workspace
bin/agentkit ask -agent workspace "Create notes/todo.md with three items, then tell me the average of 4, 8 and 15."
claude mcp add --transport http agentkit-files http://localhost:8080/mcp/files --header "Authorization: Bearer $AGENTKIT_API_TOKEN"   # or use them from Claude Code
```

`edit_file` is a write tool (kind `write`, risk `medium`); the rest are read-only. Until tool policies (T083) land, anyone with the API token can change files under `files`' root: give it a root you are happy for them to change.

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

Settings: `url`; `transport` (`auto`, the default: Streamable HTTP, then the older HTTP+SSE; or `http`, `sse`, `stdio`); `command`, `args` and `env` for stdio (`env` names the variables the command gets; it gets `PATH` and `HOME` and nothing else); `headers` (`${NAME}` is replaced by the variable; a header carrying a secret must use one, and headers go only over https or to this machine); `timeout` per call (default `60s`); `connect_timeout` (default `15s`); `max_result_bytes` (default 1 MiB). `include` and `exclude` pick tools, as for any toolset. OAuth sign-in to a server is T095.

The server is contacted at startup: one that does not answer fails startup, or with `"optional": true` is skipped and named in `/readyz`. Tools keep the server's names; an agent using two servers that both have a `search` sets `"prefix_tool_names": true` to get `linear__search` and `notion__search`. The server's descriptions are cleaned of hidden characters, its annotations can raise a tool's risk but not lower it, and its results are treated as third-party data.

**Pinning.** A server can change a tool's description after you trusted it (a "rug pull"). Two guards:

- While running, when the server says its tool list changed, every tool whose definition now differs from the one the agent was given is disabled: calls fail with "the MCP server changed this tool's definition", and the log names the tools. New tools appear only after a restart.
- `pins` fixes which definitions are allowed at all, across restarts. Print the current ones with `agentkit mcp-tools <url>` (add `-header "Authorization: Bearer ..."` if the server needs it) and paste the `"pins"` block into the toolset's settings. A tool that is not pinned, or whose definition has changed since, is left out with a warning, so only definitions you have looked at reach the model.

## Troubleshooting

- **`startup: AGENTKIT_API_TOKEN ...`**: set a token of at least 16 bytes when listening on a non-loopback address (always the case in Docker).
- **401 from `/mcp/...` in Claude Code**: add the token with `--header "Authorization: Bearer $AGENTKIT_API_TOKEN"` (`claude mcp add`). A 404 for a toolset that exists means the key does not allow it.
- **`run_error` with `model_auth`**: the API key is missing or wrong.
- **Port in use**: `PORT=9090 scripts/try-agent.sh`.
- **Logs**: JSON on stderr; the script writes them to `bin/try-agent.log`. `AGENTKIT_LOG_LEVEL=debug` for more.
