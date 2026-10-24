#!/usr/bin/env bash
# Try the agentkit server locally: the agent API (Claude + our harness + tools)
# and the MCP toolsets, in one process. Works with the stock bash on macOS.
#
#   scripts/try-agent.sh             build, start, run a short demo (curl, then agentkit ask),
#                                    keep serving (Ctrl-C stops); then try bin/agentkit chat
#   scripts/try-agent.sh --demo      build, start, run the demo, stop
#   scripts/try-agent.sh --docker    same, but run the container image instead of the binary
#   scripts/try-agent.sh --agent search
#                                    talk to that agent (default: the config's default
#                                    agent); the config then defaults to search.json,
#                                    which has the agents "default", "search", "workspace"
#                                    and "support" (answers from the sample knowledge base)
#
# Needs: Go (any recent version; the right toolchain is fetched from go.mod) or
# Docker for --docker, curl, and an Anthropic key in ANTHROPIC_API_KEY (or
# ANTHROPIC_KEY). Optional: PORT (default 8080), AGENTKIT_API_TOKEN (default:
# random), AGENTKIT_CONFIG (default deploy/local/agent/config.json, or
# deploy/local/agent/search.json with --agent).
set -euo pipefail
cd "$(dirname "$0")/.."

mode=serve
agent=""
while [ $# -gt 0 ]; do
  case "$1" in
    --demo) mode=demo ;;
    --docker) use_docker=1 ;;
    --agent) [ $# -ge 2 ] || { echo "--agent needs a name" >&2; exit 2; }; agent="$2"; shift ;;
    --agent=*) agent="${1#--agent=}" ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done
use_docker=${use_docker:-0}
case "$agent" in
  *[!a-zA-Z0-9_-]*) echo "--agent $agent: a name is letters, digits, _ and -" >&2; exit 2 ;;
esac

die() { echo "try-agent: $*" >&2; exit 1; }

key="${ANTHROPIC_API_KEY:-${ANTHROPIC_KEY:-}}"
[ -n "$key" ] || die "set ANTHROPIC_API_KEY (or ANTHROPIC_KEY) to your Anthropic API key"
command -v curl >/dev/null || die "curl is required"

port="${PORT:-8080}"
base="http://127.0.0.1:${port}"
default_config=deploy/local/agent/config.json
[ -z "$agent" ] || default_config=deploy/local/agent/search.json
config="${AGENTKIT_CONFIG:-$default_config}"
[ -f "$config" ] || die "config file not found: $config"
token="${AGENTKIT_API_TOKEN:-}"
if [ -z "$token" ]; then
  token="$(openssl rand -hex 16 2>/dev/null || true)"
  [ -n "$token" ] || token="try-agent-$(date +%s)-$$-local"
fi
# The credential vault's key: search.json's Exa and Parallel keep each
# user's own key there (memory backend: gone when this stops).
creds_key="${AGENTKIT_CREDENTIALS_KEY:-}"
if [ -z "$creds_key" ] && command -v openssl >/dev/null; then
  creds_key="k1:$(openssl rand -base64 32)"
fi
mkdir -p bin
log="bin/try-agent.log"

pid=""
container=""
cleanup() {
  if [ -n "$pid" ]; then kill -TERM "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
  if [ -n "$container" ]; then docker stop -t 20 "$container" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

if [ "$use_docker" = 1 ]; then
  command -v docker >/dev/null || die "docker is required for --docker"
  echo "==> building the image (make image)"
  make image >/dev/null
  container="agentkit-try-$$"
  # Skill folders and knowledge bases sit next to the config, and its paths
  # are relative to it.
  skills=()
  config_dir="$(cd "$(dirname "$config")" && pwd)"
  [ ! -d "$config_dir/skills" ] || skills=(-v "$config_dir/skills:/etc/agentkit/skills:ro")
  [ ! -d "$config_dir/knowledge" ] || skills+=(-v "$config_dir/knowledge:/etc/agentkit/knowledge:ro")
  echo "==> starting container $container on port $port"
  docker run -d --rm --name "$container" --stop-timeout 20 -p "${port}:8080" ${skills[@]+"${skills[@]}"} \
    -e ANTHROPIC_API_KEY="$key" -e AGENTKIT_API_TOKEN="$token" -e AGENTKIT_CREDENTIALS_KEY="$creds_key" \
    -e AGENTKIT_COMPONENTS=agent,web,mcp-server \
    -e TAVILY_API_KEY -e EXA_API_KEY -e PARALLEL_API_KEY -e HUBSPOT_ACCESS_TOKEN -e SALESFORCE_ACCESS_TOKEN \
    -v "$PWD/$config:/etc/agentkit/config.json:ro" \
    agentkit:latest >/dev/null
  log="(docker logs $container)"
else
  command -v go >/dev/null || die "Go is required (brew install go), or use --docker"
  echo "==> building bin/agentkit"
  go build -o bin/agentkit ./cmd/agentkit
  echo "==> starting agentkit serve on 127.0.0.1:${port} (log: $log)"
  ANTHROPIC_API_KEY="$key" AGENTKIT_API_TOKEN="$token" AGENTKIT_CREDENTIALS_KEY="$creds_key" \
    AGENTKIT_COMPONENTS=agent,web,mcp-server AGENTKIT_ADDR="127.0.0.1:${port}" \
    bin/agentkit serve -config "$config" >"$log" 2>&1 &
  pid=$!
fi

printf "==> waiting for readiness"
ready=0
for _ in $(seq 1 100); do
  if curl -sf "$base/readyz" >/dev/null 2>&1; then ready=1; break; fi
  printf "."
  sleep 0.2
done
echo
[ "$ready" = 1 ] || { [ -n "$pid" ] && cat "$log" >&2; die "server did not become ready"; }
curl -s "$base/readyz"; echo

auth="Authorization: Bearer $token"

echo
echo "==> 0. the agents and toolsets this server has"
curl -s "$base/v1/agents" -H "$auth" | grep -oE '"name":"[^"]*","description"' | cut -d'"' -f4 | sed 's/^/   agent: /'
curl -s "$base/v1/toolsets" -H "$auth" | grep -oE '"name":"[^"]*","type":"[^"]*","status":"[^"]*"' \
  | awk -F'"' '{print "   toolset: " $4 " (" $8 ", " $12 ")"}'

if [ -n "$agent" ]; then
  first="{\"message\":\"Which tools do you have? Answer in one short sentence.\",\"agent\":\"$agent\"}"
  echo
  echo "==> 1. a streamed run of agent $agent (SSE event types, then the answer)"
else
  first='{"message":"Use the calculate tool to compute 17 * 23. Answer with just the number."}'
  echo
  echo "==> 1. a streamed agent run that needs a tool (SSE event types, then the answer)"
fi
out="$(curl -sN "$base/v1/runs" -H "$auth" -d "$first")"
if echo "$out" | grep -q '"error"' && ! echo "$out" | grep -q '^event:'; then
  die "the run was refused: $out"
fi
echo "$out" | grep '^event:' | sed 's/^event: /   /' | uniq -c
conv="$(echo "$out" | grep -o '"conversation_id":"[^"]*"' | head -1 | cut -d'"' -f4)"
echo "   answer: $(echo "$out" | grep '"type":"run_done"' | grep -o '"answer":"[^"]*"' | cut -d'"' -f4)"
echo "   conversation: $conv"

echo
echo "==> 2. a follow-up in the same conversation (JSON instead of a stream)"
curl -s "$base/v1/runs" -H "$auth" -H 'Accept: application/json' \
  -d "{\"message\":\"Add 9 to that. Just the number.\",\"conversation_id\":\"$conv\"}" \
  | grep -oE '"(status|answer)":"[^"]*"' | sed 's/^/   /'

echo
echo "==> 3. the conversations, newest first"
curl -s "$base/v1/conversations?limit=5" -H "$auth" | grep -oE '"id":"[^"]*","agent":"[^"]*","title":"[^"]*"' \
  | awk -F'"' '{print "   " $4 " (" $8 "): " $12}'

echo
echo "==> 4. the demo tools over MCP at /mcp/demo"
curl -s "$base/mcp/demo" -H "$auth" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  | grep -oE '"name":"[a-z_]+"' | cut -d'"' -f4 | sed 's/^/   tool: /' | sort -u

echo
echo "==> 5. no token is refused"
echo "   status $(curl -s -o /dev/null -w '%{http_code}' "$base/v1/runs" -d '{"message":"hi"}')"

# The CLI client: the binary we built, or the one in the container.
if [ -n "$container" ]; then
  cli() { docker exec -e AGENTKIT_API_TOKEN="$token" -e AGENTKIT_SERVER=http://127.0.0.1:8080 "$container" /app "$@"; }
else
  cli() { AGENTKIT_API_TOKEN="$token" AGENTKIT_SERVER="$base" bin/agentkit "$@"; }
fi
agent_flag=()
[ -z "$agent" ] || agent_flag=(-agent "$agent")

echo
echo "==> 6. agentkit ask -json: the same run from the CLI, one event per line (types shown)"
cli ask -json ${agent_flag[@]+"${agent_flag[@]}"} "Use the calculate tool to compute 6 * 7. Answer with just the number." \
  | grep -o '"type":"[a-z_]*"' | head -1000 | cut -d'"' -f4 | uniq -c | sed 's/^/   /'
echo "   answer: $(cli ask ${agent_flag[@]+"${agent_flag[@]}"} "What is 2 + 2? Answer with just the number.")"

if [ "$mode" = demo ]; then
  echo
  echo "==> demo done; stopping"
  exit 0
fi

cat <<INFO

The server keeps running at $base. Ctrl-C stops it. Log: $log

Chat in the browser: open $base and paste the token ($token).

  export AGENTKIT_API_TOKEN=$token AGENTKIT_SERVER=$base
  bin/agentkit chat                                                # talk to the agent (/help for commands)
  bin/agentkit ask -json "What time is it?"                        # one run, as JSONL events (exit 0 done, 1 failed)
  curl -sN $base/v1/runs -H "Authorization: Bearer \$AGENTKIT_API_TOKEN" -d '{"message":"What time is it?"}'
  curl -s  $base/v1/conversations/$conv -H "Authorization: Bearer \$AGENTKIT_API_TOKEN"
  curl -s  $base/v1/agents -H "Authorization: Bearer \$AGENTKIT_API_TOKEN"        # the agents; a run picks one with "agent"
  curl -s  $base/v1/toolsets -H "Authorization: Bearer \$AGENTKIT_API_TOKEN"      # the toolsets: ready or skipped, and why
  claude mcp add --transport http agentkit-demo $base/mcp/demo     # use the tools from Claude Code
  claude mcp add --transport http agentkit $base/mcp/agentkit --header "Authorization: Bearer \$AGENTKIT_API_TOKEN"   # drive the agents from Claude Code

INFO
if [ -n "$pid" ]; then wait "$pid"; else docker wait "$container" >/dev/null; fi
