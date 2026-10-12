#!/usr/bin/env bash
# Try the agentkit server locally: the agent API (Claude + our harness + tools)
# and the MCP toolsets, in one process. Works with the stock bash on macOS.
#
#   scripts/try-agent.sh             build, start, run a short demo, keep serving (Ctrl-C stops)
#   scripts/try-agent.sh --demo      build, start, run the demo, stop
#   scripts/try-agent.sh --docker    same, but run the container image instead of the binary
#
# Needs: Go (any recent version; the right toolchain is fetched from go.mod) or
# Docker for --docker, curl, and an Anthropic key in ANTHROPIC_API_KEY (or
# ANTHROPIC_KEY). Optional: PORT (default 8080), AGENTKIT_API_TOKEN (default:
# random), AGENTKIT_CONFIG (default deploy/local/agent/config.json).
set -euo pipefail
cd "$(dirname "$0")/.."

mode=serve
for arg in "$@"; do
  case "$arg" in
    --demo) mode=demo ;;
    --docker) use_docker=1 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) echo "unknown argument: $arg (try --help)" >&2; exit 2 ;;
  esac
done
use_docker=${use_docker:-0}

die() { echo "try-agent: $*" >&2; exit 1; }

key="${ANTHROPIC_API_KEY:-${ANTHROPIC_KEY:-}}"
[ -n "$key" ] || die "set ANTHROPIC_API_KEY (or ANTHROPIC_KEY) to your Anthropic API key"
command -v curl >/dev/null || die "curl is required"

port="${PORT:-8080}"
base="http://127.0.0.1:${port}"
config="${AGENTKIT_CONFIG:-deploy/local/agent/config.json}"
[ -f "$config" ] || die "config file not found: $config"
token="${AGENTKIT_API_TOKEN:-}"
if [ -z "$token" ]; then
  token="$(openssl rand -hex 16 2>/dev/null || true)"
  [ -n "$token" ] || token="try-agent-$(date +%s)-$$-local"
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
  echo "==> starting container $container on port $port"
  docker run -d --rm --name "$container" --stop-timeout 20 -p "${port}:8080" \
    -e ANTHROPIC_API_KEY="$key" -e AGENTKIT_API_TOKEN="$token" \
    -e AGENTKIT_COMPONENTS=agent,mcp-server \
    -v "$PWD/$config:/etc/agentkit/config.json:ro" \
    agentkit:latest >/dev/null
  log="(docker logs $container)"
else
  command -v go >/dev/null || die "Go is required (brew install go), or use --docker"
  echo "==> building bin/agentkit"
  go build -o bin/agentkit ./cmd/agentkit
  echo "==> starting agentkit serve on 127.0.0.1:${port} (log: $log)"
  ANTHROPIC_API_KEY="$key" AGENTKIT_API_TOKEN="$token" \
    AGENTKIT_COMPONENTS=agent,mcp-server AGENTKIT_ADDR="127.0.0.1:${port}" \
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
echo "==> 1. a streamed agent run that needs a tool (SSE event types, then the answer)"
out="$(curl -sN "$base/v1/runs" -H "$auth" \
  -d '{"message":"Use the calculate tool to compute 17 * 23. Answer with just the number."}')"
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
echo "==> 3. the same tools over MCP at /mcp/demo"
curl -s "$base/mcp/demo" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  | grep -oE '"name":"[a-z_]+"' | cut -d'"' -f4 | sed 's/^/   tool: /' | sort -u

echo
echo "==> 4. no token is refused"
echo "   status $(curl -s -o /dev/null -w '%{http_code}' "$base/v1/runs" -d '{"message":"hi"}')"

if [ "$mode" = demo ]; then
  echo
  echo "==> demo done; stopping"
  exit 0
fi

cat <<INFO

The server keeps running at $base. Ctrl-C stops it. Log: $log

  export AGENTKIT_API_TOKEN=$token
  curl -sN $base/v1/runs -H "Authorization: Bearer \$AGENTKIT_API_TOKEN" -d '{"message":"What time is it?"}'
  curl -s  $base/v1/conversations/$conv -H "Authorization: Bearer \$AGENTKIT_API_TOKEN"
  claude mcp add --transport http agentkit-demo $base/mcp/demo     # use the tools from Claude Code

INFO
if [ -n "$pid" ]; then wait "$pid"; else docker wait "$container" >/dev/null; fi
