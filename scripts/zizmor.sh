#!/bin/sh
# Audits .github/ with zizmor (https://docs.zizmor.sh): unpinned actions,
# credentials left by checkout, template injection, excessive permissions.
# Runs zizmor ZIZMOR_VERSION if it is on PATH at that version, else through
# uv; with neither it says so and passes, so make check works without it.
# CI installs it and sets ZIZMOR_REQUIRED, so there a missing zizmor fails.
# Policy: .github/zizmor.yml.
set -eu
want=${ZIZMOR_VERSION:?ZIZMOR_VERSION is not set}
if command -v zizmor >/dev/null 2>&1 && [ "$(zizmor --version)" = "zizmor $want" ]; then
	set -- zizmor
elif command -v uv >/dev/null 2>&1; then
	set -- uv tool run "zizmor@$want"
elif [ -n "${ZIZMOR_REQUIRED:-}" ]; then
	echo "zizmor.sh: zizmor $want not found and ZIZMOR_REQUIRED is set" >&2
	exit 1
else
	echo "zizmor.sh: zizmor $want and uv not found; skipping (CI runs it)" >&2
	exit 0
fi
# Offline: the audits that call the GitHub API need a token, and make check
# must not depend on the network beyond fetching the tool.
exec "$@" --offline --config .github/zizmor.yml .github/
