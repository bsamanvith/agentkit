#!/usr/bin/env bash
# The split deployment (docs/COMPONENTS.md) in Docker Compose:
# deploy/compose/compose.yaml, with Postgres and the console on
# http://localhost:30081. The twin of scripts/k8s-local.sh (make k8s-up, on
# 30080): the same images, config and environment. See deploy/compose/README.md.
#
#   scripts/compose-local.sh up [allinone]     build the images, start, wait until usable
#   scripts/compose-local.sh status            containers, health and the console's URLs
#   scripts/compose-local.sh token             print the API token the console asks for
#   scripts/compose-local.sh logs [svc]        follow a service's logs (api with migrate, runner,
#                                              toolsets, postgres, allinone)
#   scripts/compose-local.sh smoke [allinone]  run the smoke suite (test/e2e) against it
#   scripts/compose-local.sh down [-v]         stop and remove the containers; -v also deletes
#                                              the volumes (Postgres, SQLite) and the secrets file
#
# allinone is one container of the agentkit-local image on SQLite, on
# http://localhost:30082, instead of the split deployment.
#
# Environment: ANTHROPIC_API_KEY (or ANTHROPIC_KEY) for the runner, stored in
# deploy/compose/.env on up; SKIP_BUILD=1 to start the images already built.
set -euo pipefail
cd "$(dirname "$0")/.."

compose_file=deploy/compose/compose.yaml
envfile=deploy/compose/.env # read by Compose itself: it sits next to compose.yaml
split_url=http://localhost:30081
allinone_url=http://localhost:30082
# Ours alone: a value of the same name in the shell would override the file.
unset AGK_RPC_TOKEN AGK_API_TOKEN AGK_PG_PASSWORD AGK_ANTHROPIC_API_KEY

die() { echo "compose-local: $*" >&2; exit 1; }
dc() { docker compose -f "$compose_file" "$@"; }

check_docker() {
	command -v docker >/dev/null || die "docker not found: install Docker Desktop"
	docker compose version >/dev/null 2>&1 || die "no docker compose v2 (docker compose version)"
	docker info >/dev/null 2>&1 || die "the Docker daemon does not answer: is Docker Desktop running?"
}

# env_get KEY prints KEY's value from the secrets file.
env_get() {
	[ -f "$envfile" ] || return 0
	sed -n "s/^$1=//p" "$envfile" | sed "s/^'\(.*\)'\$/\1/"
}

# need_env lets commands that read no secret (status, logs, down) run without
# the secrets file: Compose interpolates the whole file whatever it does.
need_env() {
	if [ ! -f "$envfile" ]; then
		export AGK_RPC_TOKEN=none AGK_API_TOKEN=none AGK_PG_PASSWORD=none
	fi
}

# ensure_env writes the secrets file with new random tokens the first time,
# and keeps them after that: Postgres was initialized with its password. A
# model key in the environment replaces the stored one (Compose then
# recreates the containers that read it). Nothing here prints a value.
ensure_env() {
	local key="${ANTHROPIC_API_KEY:-${ANTHROPIC_KEY:-}}"
	case "$key" in *"'"* | *$'\n'*) die "ANTHROPIC_API_KEY holds a quote or a newline" ;; esac
	if [ ! -f "$envfile" ]; then
		(
			umask 077
			{
				echo "# Written by scripts/compose-local.sh up; kept until down -v. Not in git."
				echo "AGK_RPC_TOKEN=$(openssl rand -hex 24)"
				echo "AGK_API_TOKEN=$(openssl rand -hex 24)"
				echo "AGK_PG_PASSWORD=$(openssl rand -hex 16)"
				echo "AGK_ANTHROPIC_API_KEY='$key'"
			} >"$envfile"
		)
		echo "compose-local: wrote $envfile" >&2
	elif [ -n "$key" ] && [ "$key" != "$(env_get AGK_ANTHROPIC_API_KEY)" ]; then
		local tmp="$envfile.tmp"
		(umask 077 && grep -v '^AGK_ANTHROPIC_API_KEY=' "$envfile" >"$tmp" && echo "AGK_ANTHROPIC_API_KEY='$key'" >>"$tmp")
		mv "$tmp" "$envfile"
		echo "compose-local: stored the new ANTHROPIC_API_KEY" >&2
	fi
}

# wait_ready URL SERVICE... waits until each service's healthcheck passes and
# URL/readyz answers 200 from this machine. With the split deployment, the
# one-shot migrate must have exited 0 first; if it failed, its logs say why.
wait_ready() {
	local url="$1" s bad
	shift
	for _ in $(seq 180); do
		bad=
		if [ "$1" != allinone ]; then
			case "$(dc ps -a --format '{{.State}} {{.ExitCode}}' migrate 2>/dev/null)" in
			"exited 0") ;;
			"exited "*) dc logs --no-log-prefix migrate >&2; die "migrate failed (logs above)" ;;
			*) bad=1 ;;
			esac
		fi
		for s in "$@"; do
			[ "$(dc ps --format '{{.Health}}' "$s" 2>/dev/null)" = healthy ] || bad=1
		done
		if [ -z "$bad" ] && curl -fs -o /dev/null --max-time 2 "$url/readyz"; then
			return 0
		fi
		sleep 1
	done
	dc --profile allinone ps -a >&2
	die "not ready after 180 s: see scripts/compose-local.sh logs <service>"
}

up() {
	local target="${1:-split}"
	[ "$target" = split ] || [ "$target" = allinone ] || die "up takes nothing or allinone, not $target"
	check_docker
	local recreate=()
	if [ "${SKIP_BUILD:-}" != 1 ]; then
		if [ "$target" = allinone ]; then make image-local; else make images-split; fi
		# The images are :latest: recreate so the rebuilt ones run.
		recreate=(--force-recreate)
	fi
	ensure_env
	if [ -z "$(env_get AGK_ANTHROPIC_API_KEY)" ]; then
		echo "compose-local: no ANTHROPIC_API_KEY: the console works, runs fail with model_auth until you set it and run up again" >&2
	fi
	if [ "$target" = allinone ]; then
		dc --profile allinone up -d ${recreate[@]+"${recreate[@]}"} allinone
		wait_ready "$allinone_url" allinone
		echo
		echo "Console: $allinone_url (all in one, SQLite)"
	else
		dc up -d ${recreate[@]+"${recreate[@]}"}
		wait_ready "$split_url" toolsets runner api
		echo
		echo "Console: $split_url"
	fi
	echo "API token (the console asks for it): $(env_get AGK_API_TOKEN)"
}

case "${1:-help}" in
up) up "${2:-}" ;;
status)
	check_docker
	need_env
	dc --profile allinone ps -a
	echo
	echo "Console: $split_url (all in one: $allinone_url); token: scripts/compose-local.sh token"
	;;
token)
	[ -f "$envfile" ] || die "no $envfile yet: run scripts/compose-local.sh up"
	env_get AGK_API_TOKEN
	;;
logs)
	check_docker
	need_env
	s="${2:-api}"
	if [ "$s" = api ]; then dc logs -f migrate api; else dc --profile allinone logs -f "$s"; fi
	;;
smoke)
	url="$split_url"
	[ "${2:-}" != allinone ] || url="$allinone_url"
	[ -f "$envfile" ] || die "no $envfile yet: run scripts/compose-local.sh up"
	AGENTKIT_URL="$url" AGENTKIT_API_TOKEN="$(env_get AGK_API_TOKEN)" go test -tags e2e -count=1 ./test/e2e/
	;;
down)
	check_docker
	need_env
	if [ "${2:-}" = -v ]; then
		dc --profile allinone down -v --remove-orphans
		# The password belongs to the Postgres volume just deleted.
		rm -f "$envfile"
	elif [ -n "${2:-}" ]; then
		die "down takes nothing or -v, not $2"
	else
		dc --profile allinone down --remove-orphans
	fi
	;;
*) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; [ "${1:-help}" = help ] ;;
esac
