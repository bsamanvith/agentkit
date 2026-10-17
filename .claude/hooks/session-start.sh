#!/bin/bash
# Prepares a Claude Code cloud session to build, lint and test agentkit,
# integration tier included (make integration): Go modules and the linter,
# the web workspace, and the Postgres and Redis binaries scripts/integration.sh
# runs as processes (there is no Docker in a cloud session). Idempotent; the
# container is cached after it, so later sessions start warm. Laptops skip it.
set -euo pipefail

if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
	exit 0
fi
cd "$CLAUDE_PROJECT_DIR"

go mod download
make --no-print-directory tools

# The console's workspace: make web-check, make web-smoke, and the
# Playwright browsers preinstalled under /opt/pw-browsers.
pnpm --dir web install --frozen-lockfile

# Postgres and Redis binaries, for scripts/integration.sh. Never started here.
missing=()
ls /usr/lib/postgresql/*/bin/initdb >/dev/null 2>&1 || command -v initdb >/dev/null || missing+=(postgresql)
command -v redis-server >/dev/null || missing+=(redis-server)
if [ ${#missing[@]} -gt 0 ]; then
	# policy-rc.d keeps apt from starting the services it installs.
	printf '#!/bin/sh\nexit 101\n' >/usr/sbin/policy-rc.d && chmod +x /usr/sbin/policy-rc.d
	{ apt-get update -qq && apt-get install -y -qq --no-install-recommends "${missing[@]}"; } ||
		echo "session-start: could not install ${missing[*]}; make integration needs them" >&2
	rm -f /usr/sbin/policy-rc.d
fi
