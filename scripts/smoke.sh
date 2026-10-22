#!/usr/bin/env bash
# The smoke suite (test/e2e, T007) against the split deployment run as plain
# processes on this machine: the agent API with the console, a runner and the
# toolsets, each built with its image's tags and wired as deploy/k8s wires
# them, with the console's fake model (web/apps/console/e2e/fake-model.ts) in
# place of the Anthropic API. No Docker, no cluster, no key. Besides the
# suite's flows through the API, TestComponents checks each process alone:
# /readyz lists its own components only, and it serves its own surface and
# none of the others' (the per-image smoke, T102).
#
#   scripts/smoke.sh            build, start, run the suite, stop
#   scripts/smoke.sh --keep     the same, then keep serving: the console is on
#                               http://127.0.0.1:$PORT (token below); Ctrl-C stops
#   scripts/smoke.sh --images   the same against the images instead of the
#                               binaries: agentkit-api, agentkit-runner and
#                               agentkit-toolsets:$IMAGE_TAG (default latest,
#                               as make images-split tags them) on the host's
#                               network. CI's image job runs it. It builds
#                               none: each image must exist (make images-split)
#                               and pass its budget (make image-size-<name>),
#                               and is never pulled
#
# The suite runs with -v and AGENTKIT_SMOKE_STRICT=1: a test that would skip
# fails, and the run fails unless every test of the suite passed by name.
#
# Environment: AGENTKIT_STORE (default: memory; a postgres:// URL names an
# empty database, which scripts/migrate-check.sh migrates twice with the api
# binary or image before the API serves it with AGENTKIT_STORE_MIGRATE=off;
# needs psql), PORT (default 18480; the others are the next three ports). Needs
# Go and Node 22.18 or later, and Docker for --images.
#
# make k8s-smoke and make compose-smoke run the same suite against make
# k8s-up's and make compose-up's deployments.
set -euo pipefail
cd "$(dirname "$0")/.."

keep= images=
for a in "$@"; do
	case "$a" in
	--keep) keep=1 ;;
	--images) images=1 ;;
	*) echo "usage: $0 [--keep] [--images]" >&2; exit 2 ;;
	esac
done
port="${PORT:-18480}"
model=$((port + 1)) toolsets=$((port + 2)) runner=$((port + 3))
dir="$(mktemp -d)"
pids=() containers=()
stop() {
	for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
	if [ ${#containers[@]} -gt 0 ]; then docker rm -f "${containers[@]}" >/dev/null 2>&1 || true; fi
	wait 2>/dev/null || true
	rm -rf "$dir"
}
trap stop EXIT

# The config of deploy/k8s/base (and deploy/compose), so the suite sees what
# the cluster runs.
cp deploy/k8s/base/config.json "$dir/config.json"
chmod 644 "$dir/config.json" # the images run as uid 65532

export AGENTKIT_RPC_TOKEN=smoke-rpc-token-0123456789
token=smoke-api-token-0123456789

if [ -n "$images" ]; then
	tag="${IMAGE_TAG:-latest}"
	for c in api runner toolsets; do
		docker image inspect "agentkit-$c:$tag" >/dev/null 2>&1 || {
			echo "smoke: no image agentkit-$c:$tag; build it (make images-split)" >&2
			exit 1
		}
		make --no-print-directory -s image-size-"$c" VERSION="$tag"
	done
	# The api image, as a release's migrate job runs it.
	api=(docker run --rm --pull=never --network host -e AGENTKIT_STORE "agentkit-api:$tag")
	start() { # start NAME ENV...: the image on the host's network, the config where it looks (Dockerfile)
		local name="$1" c="agentkit-smoke-$1-$$" e=()
		shift
		for kv in "$@"; do e+=(-e "$kv"); done
		docker run -d --pull=never --name "$c" --network host -e AGENTKIT_RPC_TOKEN "${e[@]}" \
			-v "$dir/config.json:/etc/agentkit/config.json:ro" "agentkit-$name:$tag" serve >/dev/null
		containers+=("$c")
	}
	logs() { for c in "${containers[@]}"; do echo "== $c"; docker logs --tail 20 "$c" 2>&1; done; }
	# running: each container is up, from the image it names (it did not exit).
	running() {
		local c s bad=0
		for c in "${containers[@]}"; do
			s="$(docker inspect -f '{{.Config.Image}} {{.Image}} running={{.State.Running}}' "$c" 2>&1)" || s="$c: $s"
			echo "smoke: $c: $s"
			case "$s" in *" running=true") ;; *) bad=1 ;; esac
		done
		return "$bad"
	}
else
	# The tags of each image (Makefile, VARIANTS).
	tags() { make --no-print-directory -s print-tags-"$1"; }
	for c in api runner toolsets; do
		go build -trimpath -tags "$(tags "$c")" -o "$dir/agentkit-$c" ./cmd/agentkit
	done
	export AGENTKIT_CONFIG="$dir/config.json"
	api=("$dir/agentkit-api")
	start() { # start NAME ENV... -- logs to $dir/NAME.log
		local name="$1"
		shift
		env "$@" "$dir/agentkit-$name" serve >"$dir/$name.log" 2>&1 &
		pids+=($!)
	}
	logs() { tail -n 20 "$dir"/*.log; }
	running() { # every process still runs
		local p bad=0
		for p in "${pids[@]}"; do kill -0 "$p" 2>/dev/null || { echo "smoke: process $p exited" >&2; bad=1; }; done
		return "$bad"
	}
fi

# Nothing else may answer on the ports, or the suite would test it instead.
for p in "$port" "$model" "$toolsets" "$runner"; do
	if curl -s -o /dev/null --max-time 2 "http://127.0.0.1:$p/"; then
		echo "smoke: 127.0.0.1:$p is already in use; stop what serves it or set PORT" >&2
		exit 1
	fi
done

(cd web/apps/console && exec node e2e/fake-model.ts "$model") >"$dir/model.log" 2>&1 &
pids+=($!)
start toolsets AGENTKIT_COMPONENTS=toolsets,mcp-server AGENTKIT_ADDR="127.0.0.1:$toolsets" \
	AGENTKIT_API_TOKEN="$token"
start runner AGENTKIT_COMPONENTS=runner AGENTKIT_ADDR="127.0.0.1:$runner" \
	AGENTKIT_TOOLSETS_URL="http://127.0.0.1:$toolsets" \
	ANTHROPIC_API_KEY=smoke-dummy-key ANTHROPIC_BASE_URL="http://127.0.0.1:$model"
store=()
if [ -n "${AGENTKIT_STORE:-}" ]; then
	export AGENTKIT_STORE
	scripts/migrate-check.sh "${api[@]}"
	store=(AGENTKIT_STORE="$AGENTKIT_STORE" AGENTKIT_STORE_MIGRATE=off)
fi
start api AGENTKIT_COMPONENTS=agent,web AGENTKIT_ADDR="127.0.0.1:$port" \
	AGENTKIT_RUNNER_URL="http://127.0.0.1:$runner" AGENTKIT_TOOLSETS_URL="http://127.0.0.1:$toolsets" \
	AGENTKIT_API_TOKEN="$token" "${store[@]}"

for url in "$model/healthz" "$toolsets/readyz" "$runner/readyz" "$port/readyz"; do
	for _ in $(seq 300); do
		curl -fs "http://127.0.0.1:$url" >/dev/null && continue 2
		sleep 0.1
	done
	echo "smoke: http://127.0.0.1:$url did not answer; logs:" >&2
	logs >&2 || true
	tail -n 20 "$dir/model.log" >&2
	exit 1
done

running || { logs >&2 || true; exit 1; }

# -count=1: never a cached result. -v: the log shows each test ran.
ok=0
AGENTKIT_URL="http://127.0.0.1:$port" AGENTKIT_API_TOKEN="$token" AGENTKIT_SMOKE_STRICT=1 \
	AGENTKIT_SMOKE_RUNNER_URL="http://127.0.0.1:$runner" AGENTKIT_SMOKE_TOOLSETS_URL="http://127.0.0.1:$toolsets" \
	go test -tags e2e -count=1 -v ./test/e2e/ 2>&1 | tee "$dir/test.log" || ok=$?
# Every test of the suite passed by name: none filtered out, none missing.
want=$(sed -n 's/^func \(Test[A-Za-z0-9_]*\)(t \*testing\.T).*/\1/p' test/e2e/*_test.go)
[ -n "$want" ] || { echo "smoke: found no tests in test/e2e" >&2; ok=1; }
for t in $want; do
	grep -q -- "^--- PASS: $t " "$dir/test.log" || { echo "smoke: $t did not pass" >&2; ok=1; }
done
# The suite ran against the processes or containers started here, which
# still run.
running || ok=1
if [ "$ok" = 0 ]; then
	what="the binaries"
	[ -z "$images" ] || what="the images agentkit-api, agentkit-runner and agentkit-toolsets:$tag"
	echo "smoke: passed: ${want//$'\n'/ } against $what"
else
	logs >&2 || true
fi
if [ -n "$keep" ]; then
	echo
	echo "Console: http://127.0.0.1:$port  token: $token  (fake model: say \"echo\" for a tool call)"
	echo "Ctrl-C stops."
	wait
fi
exit "$ok"
