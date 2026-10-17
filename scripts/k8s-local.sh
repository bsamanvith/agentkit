#!/usr/bin/env bash
# The split deployment (docs/COMPONENTS.md) on the Kubernetes cluster that
# Docker Desktop runs: deploy/k8s/local, with Postgres and the console on
# http://localhost:30080. It never creates a cluster, and every kubectl call
# names the context, so it cannot touch another one. See deploy/k8s/README.md.
#
#   scripts/k8s-local.sh up        build the images, deploy, wait until ready
#   scripts/k8s-local.sh status    pods, services and the console's URL
#   scripts/k8s-local.sh token     print the API token the console asks for
#   scripts/k8s-local.sh logs [c]  follow a component's logs (api, runner, toolsets, postgres)
#   scripts/k8s-local.sh smoke     run the smoke suite (test/e2e) against it
#   scripts/k8s-local.sh down      delete the namespace: pods, secret and data
#
# Environment: KUBE_CONTEXT (default docker-desktop); ANTHROPIC_API_KEY (or
# ANTHROPIC_KEY) for the runner, stored in the secret on up; SKIP_BUILD=1 to
# deploy the images already built.
set -euo pipefail
cd "$(dirname "$0")/.."

ctx="${KUBE_CONTEXT:-docker-desktop}"
ns=agentkit
overlay=deploy/k8s/local
url=http://localhost:30080
deployments=(toolsets runner api)

k() { kubectl --context "$ctx" "$@"; }
kn() { kubectl --context "$ctx" -n "$ns" "$@"; }
die() { echo "k8s-local: $*" >&2; exit 1; }

check_context() {
	command -v kubectl >/dev/null || die "kubectl not found (Docker Desktop installs it)"
	k config get-contexts -o name | grep -qx "$ctx" ||
		die "no kubectl context $ctx: turn on Kubernetes in Docker Desktop (Settings, Kubernetes), or set KUBE_CONTEXT"
	k get --raw /readyz >/dev/null 2>&1 || die "the cluster of context $ctx does not answer: is Docker Desktop running?"
}

# secret_value KEY prints the secret's KEY, decoded.
secret_value() {
	kn get secret agentkit -o go-template="{{index .data \"$1\" | base64decode}}"
}

# ensure_secret creates the secret with new random tokens the first time, and
# keeps them after that: Postgres was initialized with its password. A model
# key in the environment replaces the stored one; it prints "changed" then.
ensure_secret() {
	local key="${ANTHROPIC_API_KEY:-${ANTHROPIC_KEY:-}}"
	if ! kn get secret agentkit >/dev/null 2>&1; then
		local d pw
		d="$(mktemp -d)"
		# Values go through files, not arguments, so they stay out of ps.
		trap 'rm -rf "$d"' RETURN
		pw="$(openssl rand -hex 16)"
		printf '%s' "$(openssl rand -hex 24)" >"$d/rpc-token"
		printf '%s' "$(openssl rand -hex 24)" >"$d/api-token"
		printf '%s' "$pw" >"$d/postgres-password"
		printf 'postgres://agentkit:%s@postgres:5432/agentkit?sslmode=disable' "$pw" >"$d/store-url"
		printf '%s' "$key" >"$d/anthropic-api-key"
		kn create secret generic agentkit --from-file="$d" >/dev/null
		echo "k8s-local: created secret $ns/agentkit" >&2
	elif [ -n "$key" ] && [ "$key" != "$(secret_value anthropic-api-key)" ]; then
		kn patch secret agentkit --type merge --patch-file /dev/stdin >/dev/null <<<"{\"stringData\":{\"anthropic-api-key\":\"$key\"}}"
		echo changed
	fi
}

up() {
	check_context
	if [ "${SKIP_BUILD:-}" != 1 ]; then
		make images-split
	fi
	k create namespace "$ns" --dry-run=client -o yaml | k apply -f - >/dev/null
	local keychanged
	keychanged="$(ensure_secret)"
	if [ -z "${ANTHROPIC_API_KEY:-${ANTHROPIC_KEY:-}}" ] && [ -z "$(secret_value anthropic-api-key)" ]; then
		echo "k8s-local: no ANTHROPIC_API_KEY: the console works, runs fail with model_auth until you set it and run up again" >&2
	fi

	# Postgres first: the API's init container migrates it.
	kn apply -k "$overlay" -l app.kubernetes.io/component=postgres
	kn rollout status statefulset/postgres --timeout=180s
	kn apply -k "$overlay"
	# The images are :latest, so a rebuild or a new key changes no pod spec:
	# restart to pick them up.
	if [ "${SKIP_BUILD:-}" != 1 ] || [ "$keychanged" = changed ]; then
		kn rollout restart "${deployments[@]/#/deployment/}"
	fi
	for d in "${deployments[@]}"; do
		kn rollout status "deployment/$d" --timeout=180s
	done
	echo
	echo "Console: $url"
	echo "API token (the console asks for it): $(secret_value api-token)"
}

status() {
	check_context
	kn get pods,services
	echo
	echo "Console: $url (token: scripts/k8s-local.sh token)"
}

case "${1:-help}" in
up) up ;;
status) status ;;
token) check_context && secret_value api-token && echo ;;
logs)
	check_context
	c="${2:-api}"
	if [ "$c" = postgres ]; then kn logs -f statefulset/postgres; else kn logs -f "deployment/$c" --all-containers; fi
	;;
smoke)
	check_context
	AGENTKIT_URL="$url" AGENTKIT_API_TOKEN="$(secret_value api-token)" go test -tags e2e -count=1 ./test/e2e/
	;;
down)
	check_context
	k delete namespace "$ns" --ignore-not-found --wait
	;;
*) sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'; [ "${1:-help}" = help ] ;;
esac
