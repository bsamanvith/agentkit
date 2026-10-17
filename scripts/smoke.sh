#!/usr/bin/env bash
# The smoke suite (test/e2e, T007) against the split deployment run as plain
# processes on this machine: the agent API with the console, a runner and the
# toolsets, each built with its image's tags and wired as deploy/k8s wires
# them, with the console's fake model (web/apps/console/e2e/fake-model.ts) in
# place of the Anthropic API. No Docker, no cluster, no key.
#
#   scripts/smoke.sh          build, start, run the suite, stop
#   scripts/smoke.sh --keep   the same, then keep serving: the console is on
#                             http://127.0.0.1:$PORT (token below); Ctrl-C stops
#
# Environment: AGENTKIT_STORE (default: memory; a postgres:// URL is migrated
# first), PORT (default 18480; the others are the next three ports). Needs Go
# and Node 22.18 or later.
#
# make k8s-smoke runs the same suite against make k8s-up's deployment.
set -euo pipefail
cd "$(dirname "$0")/.."

keep=
[ "${1:-}" = --keep ] && keep=1
port="${PORT:-18480}"
model=$((port + 1)) toolsets=$((port + 2)) runner=$((port + 3))
dir="$(mktemp -d)"
pids=()
stop() {
	for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
	wait 2>/dev/null || true
	rm -rf "$dir"
}
trap stop EXIT

# The tags of each image (Makefile, VARIANTS).
tags() { make --no-print-directory -s print-tags-"$1"; }
for c in api runner toolsets; do
	go build -trimpath -tags "$(tags "$c")" -o "$dir/agentkit-$c" ./cmd/agentkit
done
# The config of deploy/k8s/base, so the suite sees what the cluster runs.
sed -n '/config.json: |/,/^---/p' deploy/k8s/base/split.yaml | sed '1d;$d;s/^    //' >"$dir/config.json"

export AGENTKIT_CONFIG="$dir/config.json"
export AGENTKIT_RPC_TOKEN=smoke-rpc-token-0123456789
token=smoke-api-token-0123456789

start() { # start NAME ENV... -- logs to $dir/NAME.log
	local name="$1"
	shift
	env "$@" "$dir/agentkit-$name" serve >"$dir/$name.log" 2>&1 &
	pids+=($!)
}
(cd web/apps/console && exec node e2e/fake-model.ts "$model") >"$dir/model.log" 2>&1 &
pids+=($!)
start toolsets AGENTKIT_COMPONENTS=toolsets,mcp-server AGENTKIT_ADDR="127.0.0.1:$toolsets" \
	AGENTKIT_API_TOKEN="$token"
start runner AGENTKIT_COMPONENTS=runner AGENTKIT_ADDR="127.0.0.1:$runner" \
	AGENTKIT_TOOLSETS_URL="http://127.0.0.1:$toolsets" \
	ANTHROPIC_API_KEY=smoke-dummy-key ANTHROPIC_BASE_URL="http://127.0.0.1:$model"
store=()
if [ -n "${AGENTKIT_STORE:-}" ]; then
	"$dir/agentkit-api" migrate
	store=(AGENTKIT_STORE="$AGENTKIT_STORE" AGENTKIT_STORE_MIGRATE=off)
fi
start api AGENTKIT_COMPONENTS=agent,web AGENTKIT_ADDR="127.0.0.1:$port" \
	AGENTKIT_RUNNER_URL="http://127.0.0.1:$runner" AGENTKIT_TOOLSETS_URL="http://127.0.0.1:$toolsets" \
	AGENTKIT_API_TOKEN="$token" "${store[@]}"

for url in "$model/healthz" "$toolsets/readyz" "$runner/readyz" "$port/readyz"; do
	for _ in $(seq 100); do
		curl -fs "http://127.0.0.1:$url" >/dev/null && continue 2
		sleep 0.1
	done
	echo "smoke: http://127.0.0.1:$url did not answer; logs:" >&2
	tail -n 20 "$dir"/*.log >&2
	exit 1
done

ok=0
AGENTKIT_URL="http://127.0.0.1:$port" AGENTKIT_API_TOKEN="$token" \
	go test -tags e2e -count=1 ./test/e2e/ || ok=$?
if [ "$ok" != 0 ]; then
	tail -n 20 "$dir"/*.log >&2
fi
if [ -n "$keep" ]; then
	echo
	echo "Console: http://127.0.0.1:$port  token: $token  (fake model: say \"echo\" for a tool call)"
	echo "Ctrl-C stops."
	wait
fi
exit "$ok"
