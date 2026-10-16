# Run a connector locally

The tests prove the contract; this proves the wiring: the binary builds the toolset from config, serves it over MCP at `/mcp/<name>`, and an agent run calls it. Full server docs: [docs/QUICKSTART.md](../../../../docs/QUICKSTART.md). Do it after step 4 of the skill (the factory is in `cmd/agentkit/catalog.go` and the toolset in a config).

## 1. A config with your toolset

Copy `deploy/local/agent/search.json` to `bin/<name>.json` (bin/ is ignored) or add to it. Keep `"optional": true` so a missing credential skips the toolset instead of failing startup:

```json
{"name": "gmail", "type": "gmail", "settings": {"access_token_env": "GOOGLE_ACCESS_TOKEN"}, "optional": true}
```

and an agent whose `toolsets` names it. Restrict tools with `"include"`/`"exclude"` globs (each must match a tool). Unknown fields anywhere fail startup.

## 2. Start the server

One command (builds, starts, runs a short demo, keeps serving; needs `ANTHROPIC_API_KEY`, or `ANTHROPIC_KEY` in Claude sessions):

```sh
export PARALLEL_API_KEY=...            # or GOOGLE_ACCESS_TOKEN=..., whatever your connector reads
AGENTKIT_CONFIG=bin/<name>.json scripts/try-agent.sh --agent <agent>
```

By hand:

```sh
go build -o bin/agentkit ./cmd/agentkit
export AGENTKIT_API_TOKEN=$(openssl rand -hex 16)   # at least 16 bytes, or startup fails
AGENTKIT_COMPONENTS=agent,web,mcp-server AGENTKIT_ADDR=127.0.0.1:8080 \
  bin/agentkit serve -config bin/<name>.json
```

`AGENTKIT_STORE=sqlite:bin/agentkit.db` keeps conversations across restarts. The startup log says each connector's auth mode (never the secret) and why an optional toolset was skipped.

## 3. Check it, cheapest first

```sh
B=http://127.0.0.1:8080; A="Authorization: Bearer $AGENTKIT_API_TOKEN"
M=(-H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream')

curl -s $B/readyz                       # "degraded" names each skipped toolset and why
curl -s $B/v1/toolsets -H "$A"          # status ready|skipped, mcp_path, tool_count, credential {status, env}

# over MCP (stateless; no initialize needed for a quick look)
curl -s $B/mcp/<name> "${M[@]}" -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
curl -s $B/mcp/<name> "${M[@]}" -d '{"jsonrpc":"2.0","id":2,"method":"tools/call",
  "params":{"name":"<tool>","arguments":{...}}}'   # result has content and structuredContent; isError for a classed failure

# an agent run that should use it (SSE; tool_call / tool_result events show the call)
curl -sN $B/v1/runs -H "$A" -d '{"agent":"<agent>","message":"..."}'
bin/agentkit ask -v -agent <agent> "..."   # AGENTKIT_SERVER=$B; -v prints tool calls and results on stderr
```

Look for: the tools you expect and no others (writes absent unless the setting is on), schemas with your limits, an invalid call coming back as an error result the model can act on, the credential's variable named in `/v1/toolsets` (if it says `not_needed`, add it to `credentialEnv`).

From an MCP client: `claude mcp add --transport http agentkit-<name> $B/mcp/<name>`, or the MCP Inspector (`npx @modelcontextprotocol/inspector`, transport Streamable HTTP, URL `$B/mcp/<name>`).

**Keep the server on loopback** (the default address) while a toolset holds a key or a user's token: `/mcp` has no per-route auth until T040, so anyone who can reach it spends the key or reads the account.

## 4. Without a credential

- Point the connector at a loopback stub with its `base_url` setting (http is allowed only for loopback) and a dummy key: the stub answers with a cassette's body. This is what `cmd/agentkit/<name>_test.go` automates with `httptest`; prefer that test over a manual stub.
- For an agent without a model key, `fakellm` (ENGINEERING §3.1) scripts the model.

## 5. The image

```sh
make image-local                         # agentkit-local: memory or SQLite, no Postgres/Redis
docker run --rm --stop-timeout 20 -p 8080:8080 \
  -e ANTHROPIC_API_KEY -e AGENTKIT_API_TOKEN -e AGENTKIT_COMPONENTS=agent,web,mcp-server \
  -e <YOUR_VAR> \
  -v "$PWD/bin/<name>.json:/etc/agentkit/config.json:ro" agentkit-local:latest
make image-size-local                    # budget IMAGE_BUDGET_MB_local; log a SIZES.md row for >= 100 KB
```

`scripts/try-agent.sh --docker` does the same with the all-in-one `make image`; add your variable to its `-e` list. Inside the container the server listens on `:8080`, so the token is required. A new top-level Go directory must be allowed in `.dockerignore`, or the image build breaks while tests pass.

## 6. Live and recording

- `make test-live` runs `live_test.go` (`TestLive`, `TestDrift`) against the real API; it spends credits. Sessions only, never CI.
- `make record-cassettes` then `make golden`; review both diffs before committing. Recording refuses to write a file holding a secret.
