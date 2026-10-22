#!/usr/bin/env bash
# The integration tests without Docker, for Claude Code cloud sessions (no
# Docker there) and laptops alike. Two stores:
#
#   STORE=sqlite (default)  light: no servers to start
#     1. the agent API's suite on SQLite
#     2. the smoke suite (scripts/smoke.sh): the split deployment as
#        processes, on the fake model (the api image has no SQLite, so its
#        store is memory)
#   STORE=postgres          what CI's integration job runs (check.yml), against
#                           a throwaway Postgres and Redis run as processes
#     1. go test -tags integration: pkg/bus, pkg/store, pkg/stream,
#        pkg/credentials, and cmd/agentkit's migrate, replica and
#        credential vault tests
#     2. the agent API's suite on SQLite and on Postgres
#     3. the smoke suite on Postgres: the api binary migrates an empty
#        database, and a second run has nothing to do
#        (scripts/migrate-check.sh); the API then serves runs on it with
#        AGENTKIT_STORE_MIGRATE=off
#   Either way, last: live tests against the real model (make test-live), only
#   when ANTHROPIC_API_KEY (or ANTHROPIC_KEY) is set.
#
# Every step runs even if an earlier one failed; the exit status says whether
# any did.
#
#   scripts/integration.sh          run the steps above
#   scripts/integration.sh --deps   STORE=postgres only: start Postgres and
#                                   Redis, print their env, wait; Ctrl-C stops
#
# Environment for STORE=postgres: PGPORT (default 15432), REDIS_PORT (default
# 16379), PG_BIN (the directory with initdb and pg_ctl; found on PATH or in
# /usr/lib/postgresql/*/bin), REDIS_SERVER (default: redis-server on PATH).
# As root, Postgres runs as the postgres user (initdb refuses root).
set -uo pipefail
cd "$(dirname "$0")/.."

deps_only=
[ "${1:-}" = --deps ] && deps_only=1
store="${STORE:-sqlite}"
pgport="${PGPORT:-15432}"
redisport="${REDIS_PORT:-16379}"

die() { echo "integration: $*" >&2; exit 1; }

status=0
failed=()
step() { # step NAME CMD...
	local name="$1"
	shift
	echo
	echo "=== $name"
	if ! "$@"; then
		failed+=("$name")
		status=1
	fi
}
live() {
	if [ -n "${ANTHROPIC_API_KEY:-${ANTHROPIC_KEY:-}}" ]; then
		# A Claude Code session sets ANTHROPIC_BASE_URL for itself; the live
		# tests talk to the real API.
		step "live: the real model" env -u ANTHROPIC_BASE_URL make --no-print-directory test-live
	else
		echo
		echo "=== live: skipped (set ANTHROPIC_API_KEY to run the real-model tests)"
	fi
}
finish() {
	echo
	if [ "$status" = 0 ]; then
		echo "integration ($store): all passed"
	else
		printf 'integration (%s): failed:' "$store" >&2
		printf ' [%s]' "${failed[@]}" >&2
		echo >&2
	fi
	exit "$status"
}

case "$store" in
sqlite)
	[ -z "$deps_only" ] || die "--deps needs STORE=postgres; SQLite has no server to start"
	step "agent API suite on SQLite" env AGENTAPI_TEST_STORE=sqlite go test -race -count=1 ./pkg/agentapi/...
	step "smoke suite (fake model)" scripts/smoke.sh
	live
	finish
	;;
postgres) ;;
*) die "STORE=$store: want sqlite or postgres" ;;
esac

pgbin="${PG_BIN:-}"
if [ -z "$pgbin" ]; then
	if command -v initdb >/dev/null; then
		pgbin="$(dirname "$(command -v initdb)")"
	else
		pgbin="$(ls -d /usr/lib/postgresql/*/bin 2>/dev/null | sort -V | tail -n 1)"
	fi
fi
[ -x "$pgbin/initdb" ] || die "no Postgres binaries (initdb, pg_ctl); set PG_BIN or install postgresql"
redis="${REDIS_SERVER:-$(command -v redis-server || true)}"
[ -x "$redis" ] || die "no redis-server; set REDIS_SERVER or install redis-server"

dir="$(mktemp -d)"
chmod 755 "$dir"
as_pg=()
if [ "$(id -u)" = 0 ]; then
	id postgres >/dev/null 2>&1 || die "running as root and there is no postgres user to run Postgres as"
	as_pg=(runuser -u postgres --)
fi
redis_pid=
stop() {
	"${as_pg[@]}" "$pgbin/pg_ctl" -D "$dir/pg" -m immediate stop >/dev/null 2>&1 || true
	[ -n "$redis_pid" ] && kill "$redis_pid" 2>/dev/null
	wait 2>/dev/null || true
	rm -rf "$dir"
}
trap stop EXIT
trap 'exit 130' INT TERM

mkdir "$dir/pg"
[ ${#as_pg[@]} -gt 0 ] && chown postgres: "$dir/pg"
"${as_pg[@]}" "$pgbin/initdb" -D "$dir/pg" -U agentkit --auth=trust -E UTF8 --locale=C >"$dir/initdb.log" 2>&1 ||
	{ cat "$dir/initdb.log" >&2; die "initdb failed"; }
# max_connections is postgres:17's default, as CI's service runs it: the
# packages' tests share it at once, and more here would hide an overrun there.
"${as_pg[@]}" "$pgbin/pg_ctl" -D "$dir/pg" -l "$dir/pg/server.log" -w \
	-o "-p $pgport -k $dir/pg -c listen_addresses=127.0.0.1 -c fsync=off -c max_connections=100" start >/dev/null ||
	{ cat "$dir/pg/server.log" >&2; die "Postgres did not start on port $pgport"; }
"${as_pg[@]}" "$pgbin/createdb" -h 127.0.0.1 -p "$pgport" -U agentkit agentkit_test ||
	die "createdb failed"

"$redis" --port "$redisport" --bind 127.0.0.1 --save '' --appendonly no >"$dir/redis.log" 2>&1 &
redis_pid=$!
for _ in $(seq 100); do
	(exec 3<>"/dev/tcp/127.0.0.1/$redisport") 2>/dev/null && break
	sleep 0.1
done
(exec 3<>"/dev/tcp/127.0.0.1/$redisport") 2>/dev/null ||
	{ cat "$dir/redis.log" >&2; die "Redis did not start on port $redisport"; }

# The variables CI's integration job sets (check.yml).
export AGENTKIT_REQUIRE_INTEGRATION=1
export AGENTKIT_REDIS_URL="redis://127.0.0.1:$redisport/0"
export AGENTKIT_REDIS_SERVER="$redis"
export AGENTKIT_PG_URL="postgres://agentkit@127.0.0.1:$pgport/agentkit_test?sslmode=disable"

if [ -n "$deps_only" ]; then
	echo "export AGENTKIT_REQUIRE_INTEGRATION=1"
	echo "export AGENTKIT_REDIS_URL=$AGENTKIT_REDIS_URL"
	echo "export AGENTKIT_REDIS_SERVER=$AGENTKIT_REDIS_SERVER"
	echo "export AGENTKIT_PG_URL='$AGENTKIT_PG_URL'"
	echo "Postgres and Redis are up; Ctrl-C stops them."
	while :; do sleep 3600; done
fi

step "integration: bus, store, stream, credentials" \
	go test -race -shuffle=on -count=1 -tags integration ./pkg/bus/... ./pkg/store/... ./pkg/stream/... ./pkg/credentials/...
step "integration: migrate, replicas and credentials" \
	go test -race -count=1 -tags integration -run '^(TestMigrate|TestE2EServeReplicas|TestE2ESteeringAcrossReplicas|TestCredentials)' ./cmd/agentkit/
step "agent API suite on SQLite" \
	env AGENTAPI_TEST_STORE=sqlite go test -race -count=1 ./pkg/agentapi/...
step "agent API suite on Postgres" \
	env AGENTAPI_TEST_STORE="$AGENTKIT_PG_URL" go test -race -count=1 ./pkg/agentapi/...
# The smoke suite gets a database of its own: its migrate must not meet the
# schemas the store tests left behind.
"${as_pg[@]}" "$pgbin/createdb" -h 127.0.0.1 -p "$pgport" -U agentkit agentkit_smoke
step "smoke suite on Postgres (fake model): migrate, then serve with AGENTKIT_STORE_MIGRATE=off" \
	env PSQL="$pgbin/psql" AGENTKIT_STORE="postgres://agentkit@127.0.0.1:$pgport/agentkit_smoke?sslmode=disable" scripts/smoke.sh
live
finish
