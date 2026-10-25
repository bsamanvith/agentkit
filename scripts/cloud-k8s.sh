#!/usr/bin/env bash
# The split deployment on Kubernetes inside a Claude Code cloud session:
# what scripts/k8s-local.sh does on Docker Desktop, on a one-node k3s that
# this script starts in the session's container. The console is on
# http://localhost:30080, as on a laptop.
#
#   scripts/cloud-k8s.sh up      start Docker and k3s, build, import, deploy
#   scripts/cloud-k8s.sh smoke   the smoke suite (test/e2e) against it
#   scripts/cloud-k8s.sh down    delete the deployment, stop k3s
#   scripts/cloud-k8s.sh compose <cmd>  scripts/compose-local.sh <cmd> (up,
#                                smoke, down, ...) with Docker started and
#                                the proxy's CA, console on 127.0.0.1:30081
#
# Any other command (status, token, logs) goes to scripts/k8s-local.sh with
# the k3s context. Why each step is needed in a cloud session:
#
#   - Docker is installed but not started, and Docker Hub rate-limits the
#     session's address: dockerd starts with mirror.gcr.io as its mirror.
#   - kind cannot start (runc fails inside its node container), so k3s runs
#     on the container itself, with its own containerd.
#   - The sandbox refuses a negative oom_score_adj, which the kubelet asks of
#     every pod sandbox: containerd's restrict_oom_score_adj clamps it.
#   - Outbound TLS goes through the session's proxy, whose CA
#     (/root/.ccr/ca-bundle.crt) the image builds and the pods must trust:
#     BUILD_CA for the builds, EXTRA_CA_FILE for the pods.
#
# Environment: ANTHROPIC_API_KEY or ANTHROPIC_KEY, as for k8s-local.sh.
set -euo pipefail
cd "$(dirname "$0")/.."

ca=/root/.ccr/ca-bundle.crt
k3s_version=v1.33.5+k3s1
kubeconfig=/etc/rancher/k3s/k3s.yaml
containerd_dir=/var/lib/rancher/k3s/agent/etc/containerd
die() { echo "cloud-k8s: $*" >&2; exit 1; }

[ "${CLAUDE_CODE_REMOTE:-}" = true ] || die "this is for Claude Code cloud sessions; on a laptop use make k8s-up"
export KUBECONFIG="$kubeconfig" KUBE_CONTEXT=default

start_docker() {
	docker info >/dev/null 2>&1 && return
	if [ ! -f /etc/docker/daemon.json ]; then
		mkdir -p /etc/docker
		echo '{"registry-mirrors":["https://mirror.gcr.io"]}' >/etc/docker/daemon.json
	fi
	nohup dockerd >/tmp/dockerd.log 2>&1 &
	timeout 60 bash -c 'until docker info >/dev/null 2>&1; do sleep 1; done' || die "dockerd did not start: /tmp/dockerd.log"
}

install_tools() {
	if ! command -v k3s >/dev/null; then
		curl -fsSL -o /usr/local/bin/k3s "https://github.com/k3s-io/k3s/releases/download/${k3s_version/+/%2B}/k3s"
		chmod +x /usr/local/bin/k3s
	fi
	command -v kubectl >/dev/null || ln -s /usr/local/bin/k3s /usr/local/bin/kubectl
	local img=/var/lib/rancher/k3s/agent/images/k3s-airgap-images-amd64.tar.zst
	if [ ! -f "$img" ]; then
		mkdir -p "$(dirname "$img")"
		curl -fsSL -o "$img" "https://github.com/k3s-io/k3s/releases/download/${k3s_version/+/%2B}/k3s-airgap-images-amd64.tar.zst"
	fi
	mkdir -p /etc/rancher/k3s
	[ -f /etc/rancher/k3s/registries.yaml ] ||
		printf 'mirrors:\n  docker.io:\n    endpoint:\n      - "https://mirror.gcr.io"\n' >/etc/rancher/k3s/registries.yaml
}

k3s_ready() { kubectl get --raw /readyz >/dev/null 2>&1; }

run_k3s() {
	nohup k3s server --disable traefik,metrics-server,servicelb --disable-helm-controller \
		--write-kubeconfig-mode 644 >/tmp/k3s.log 2>&1 &
}

stop_k3s() {
	pkill -x k3s-server || pkill -x k3s || true
	timeout 60 bash -c 'while pgrep -x k3s-server >/dev/null || pgrep -x k3s >/dev/null; do sleep 1; done'
}

start_k3s() {
	k3s_ready && return
	if [ ! -f "$containerd_dir/config-v3.toml.tmpl" ]; then
		# The first start writes containerd's config; the template is that
		# config with restrict_oom_score_adj, used from the second start on.
		run_k3s
		timeout 120 bash -c "until [ -f $containerd_dir/config.toml ]; do sleep 1; done" || die "k3s wrote no containerd config: /tmp/k3s.log"
		stop_k3s
		sed -e '1d' -e "/^\[plugins.'io.containerd.cri.v1.runtime'\]\$/a\\  restrict_oom_score_adj = true" \
			"$containerd_dir/config.toml" >"$containerd_dir/config-v3.toml.tmpl"
		grep -q restrict_oom_score_adj "$containerd_dir/config-v3.toml.tmpl" || die "no CRI runtime section in containerd's config"
	fi
	run_k3s
	timeout 240 bash -c 'until kubectl -n kube-system get pods 2>/dev/null | grep -q "coredns.*1/1"; do sleep 3; done' ||
		die "k3s did not come up: /tmp/k3s.log"
}

up() {
	start_docker
	install_tools
	start_k3s
	make images-split BUILD_CA="$ca"
	for c in api runner toolsets; do
		docker save "agentkit-$c:latest" | k3s ctr -n k8s.io images import - >/dev/null
	done
	EXTRA_CA_FILE="$ca" FORCE_RESTART=1 SKIP_BUILD=1 scripts/k8s-local.sh up
}

down() {
	if k3s_ready; then
		scripts/k8s-local.sh down
	fi
	stop_k3s
	# What k3s-killall.sh does: the pods' processes outlive k3s, and their
	# mounts outlive them.
	ps -eo pid=,args= | awk '/containerd-shim/ && /k3s/ {print $1}' | xargs -r kill
	mount | awk '$3 ~ "^(/var/lib/kubelet|/run/k3s|/var/lib/rancher/k3s)/" {print $3}' | sort -r | xargs -r umount -l
}

case "${1:-help}" in
up) up ;;
down) down ;;
compose)
	shift
	start_docker
	BUILD_CA="$ca" EXTRA_CA_FILE="$ca" scripts/compose-local.sh "$@"
	;;
help | -h) sed -n '2,27p' "$0" | sed 's/^# \{0,1\}//' ;;
*) scripts/k8s-local.sh "$@" ;;
esac
