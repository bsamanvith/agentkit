#!/usr/bin/env bash
# The split deployment (docs/COMPONENTS.md) on the Kubernetes cluster that
# Docker Desktop runs: deploy/k8s/local, with Postgres and the console on
# http://localhost:30080. It never creates a cluster, and every kubectl call
# names the context, so it cannot touch another one. See deploy/k8s/README.md.
#
#   scripts/k8s-local.sh up        build the images, deploy, wait until ready
#   scripts/k8s-local.sh status    pods, services and the console's URL
#   scripts/k8s-local.sh login     print the console's sign-ins: user admin, API token
#   scripts/k8s-local.sh token     print the API token
#   scripts/k8s-local.sh logs [c]  follow a component's logs (api, runner, toolsets, postgres)
#   scripts/k8s-local.sh smoke     run the smoke suite (test/e2e) against it
#   scripts/k8s-local.sh down      delete the namespace: pods, secret and data
#
# Environment: KUBE_CONTEXT (default docker-desktop); ANTHROPIC_API_KEY (or
# ANTHROPIC_KEY) for the runner, stored in the secret on up; SKIP_BUILD=1 to
# deploy the images already built (FORCE_RESTART=1 restarts the pods anyway,
# for images loaded into the cluster another way); EXTRA_CA_FILE, a PEM file
# of CAs the pods trust too, behind a TLS-intercepting proxy
# (scripts/cloud-k8s.sh sets these).
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
	# A here-string, not a pipe: grep -q exits at the first match, and under
	# pipefail kubectl's SIGPIPE would fail the check when the context is
	# not the last one listed.
	grep -qx "$ctx" <<<"$(k config get-contexts -o name)" ||
		die "no kubectl context $ctx: turn on Kubernetes in Docker Desktop (Settings, Kubernetes), or set KUBE_CONTEXT"
	k get --raw /readyz >/dev/null 2>&1 || die "the cluster of context $ctx does not answer: is Docker Desktop running?"
}

# secret_value KEY prints the secret's KEY, decoded.
secret_value() {
	kn get secret agentkit -o go-template="{{with index .data \"$1\"}}{{base64decode .}}{{end}}"
}

# ensure_secret creates the secret with new random tokens the first time, and
# keeps them after that: Postgres was initialized with its password. A model
# key or an OAuth app in the environment replaces the stored one; it prints
# "changed" then.
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
		printf 'k1:%s' "$(openssl rand -base64 32)" >"$d/credentials-key"
		kn create secret generic agentkit --from-file="$d" >/dev/null
		echo "k8s-local: created secret $ns/agentkit" >&2
	elif [ -n "$key" ] && [ "$key" != "$(secret_value anthropic-api-key)" ]; then
		kn patch secret agentkit --type merge --patch-file /dev/stdin >/dev/null <<<"{\"stringData\":{\"anthropic-api-key\":\"$key\"}}"
		echo changed
	fi
	# A secret from before the credential vault gets its key ring.
	if [ -z "$(secret_value credentials-key)" ]; then
		kn patch secret agentkit --type merge --patch-file /dev/stdin >/dev/null <<<"{\"stringData\":{\"credentials-key\":\"k1:$(openssl rand -base64 32)\"}}"
		echo changed
	fi
	# The console's user, admin, who connects their own accounts (the API
	# token has no user): the secret agentkit-console, a random password and
	# a users file with its argon2id hash for $AGENTKIT_USERS_FILE, made by
	# the API image itself (agentkit users hash-password).
	if ! kn get secret agentkit-console >/dev/null 2>&1; then
		local c cpw hash
		c="$(mktemp -d)"
		cpw="$(openssl rand -hex 12)"
		hash="$(printf '%s\n' "$cpw" | docker run --rm -i --network none agentkit-api:latest users hash-password)" ||
			die "could not hash the console's password with agentkit-api:latest"
		printf '%s' "$cpw" >"$c/password"
		printf '{"users": [{"username": "admin", "password_hash": "%s", "tenant": "default", "roles": ["admin"]}]}' "$hash" >"$c/users.json"
		kn create secret generic agentkit-console --from-file="$c" >/dev/null
		rm -rf "$c"
		echo "k8s-local: created secret $ns/agentkit-console, the console's user admin" >&2
		echo changed
	fi
	# The OAuth apps of the Connectors page, from the environment when set
	# (docs/QUICKSTART.md "Connecting accounts").
	local v name val
	for v in GOOGLE_OAUTH_CLIENT_ID GOOGLE_OAUTH_CLIENT_SECRET HUBSPOT_OAUTH_CLIENT_ID HUBSPOT_OAUTH_CLIENT_SECRET \
		SALESFORCE_OAUTH_CLIENT_ID SALESFORCE_OAUTH_CLIENT_SECRET; do
		val="${!v:-}"
		name="$(tr '[:upper:]_' '[:lower:]-' <<<"$v")"
		case "$val" in *'"'* | *'\'* | *$'\n'*) echo "k8s-local: $v holds a quote, backslash or newline; skipped" >&2 && continue ;; esac
		if [ -n "$val" ] && [ "$val" != "$(secret_value "$name")" ]; then
			kn patch secret agentkit --type merge --patch-file /dev/stdin >/dev/null <<<"{\"stringData\":{\"$name\":\"$val\"}}"
			echo changed
		fi
	done
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

	# The handbook knowledge base's documents (deploy/k8s/base/split.yaml).
	# The toolsets component reads them once, when it starts: restart it
	# when they changed.
	local kb
	kb="$(kn create configmap agentkit-knowledge --from-file=deploy/local/agent/knowledge \
		--dry-run=client -o yaml | kn apply -f -)"
	# Postgres first: the API's init container migrates it.
	kn apply -k "$overlay" -l app.kubernetes.io/component=postgres
	kn rollout status statefulset/postgres --timeout=180s
	kn apply -k "$overlay"
	# The images are :latest, so a rebuild or a new key changes no pod spec:
	# restart to pick them up, and after new knowledge documents too. One at
	# a time, in the order they depend on each other: the runner and the API
	# read the toolsets' list once, at startup, and must not read it from the
	# old toolsets pod.
	local restart=
	if [ "${SKIP_BUILD:-}" != 1 ] || [ "${FORCE_RESTART:-}" = 1 ] || [ -n "$keychanged" ] || [[ "$kb" == *configured* ]]; then
		restart=1
	fi
	if [ -n "${EXTRA_CA_FILE:-}" ]; then
		kn create configmap agentkit-extra-ca --from-file=ca.crt="$EXTRA_CA_FILE" \
			--dry-run=client -o yaml | kn apply -f - >/dev/null
	fi
	for d in "${deployments[@]}"; do
		if [ -n "${EXTRA_CA_FILE:-}" ]; then
			# Go reads every file of SSL_CERT_DIR's folders: the image's
			# CAs, then the extra ones.
			kn patch "deployment/$d" --type strategic -p '{"spec":{"template":{"spec":{
				"volumes":[{"name":"extra-ca","configMap":{"name":"agentkit-extra-ca"}}],
				"containers":[{"name":"'"$d"'",
					"env":[{"name":"SSL_CERT_DIR","value":"/etc/ssl/certs:/etc/ssl/extra"}],
					"volumeMounts":[{"name":"extra-ca","mountPath":"/etc/ssl/extra","readOnly":true}]}]}}}}' >/dev/null
		fi
		if [ -n "$restart" ]; then
			kn rollout restart "deployment/$d" >/dev/null
		fi
		kn rollout status "deployment/$d" --timeout=180s
	done
	echo
	echo "Console: $url"
	login
}

# login prints the console's sign-ins: the user's, and the API token.
login() {
	echo "Console sign-in: admin, password $(kn get secret agentkit-console -o go-template='{{with .data.password}}{{base64decode .}}{{end}}') (connects your own accounts and keys)"
	echo "API token (the console accepts it too; it has no user, so it can't connect accounts): $(secret_value api-token)"
}

status() {
	check_context
	kn get pods,services
	echo
	echo "Console: $url (sign-in: scripts/k8s-local.sh login)"
}

case "${1:-help}" in
up) up ;;
status) status ;;
token) check_context && secret_value api-token && echo ;;
login) check_context && login ;;
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
*) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; [ "${1:-help}" = help ] ;;
esac
