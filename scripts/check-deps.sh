#!/usr/bin/env bash
# Fail if an image variant's binary links what its build tags are there to
# leave out (T102). `make deps-check` runs it with each variant's tags in
# TAGS_<variant>; the Makefile's VARIANTS say what each image is for.
#
#   scripts/check-deps.sh local dev api runner toolsets
set -euo pipefail
cd "$(dirname "$0")/.."

anthropic='github.com/anthropics/anthropic-sdk-go'
mcp='github.com/modelcontextprotocol/go-sdk'
pgx='github.com/jackc/pgx'
sqlite='modernc.org/sqlite'
redis='github.com/redis/go-redis'
connectors='github.com/bsamanvith/agentkit/connectors/'
skill='github.com/bsamanvith/agentkit/pkg/skill'

# What each variant must not link.
forbidden() {
	case "$1" in
	local) echo "$pgx $redis" ;;
	dev) echo "$sqlite" ;;
	api) echo "$anthropic $mcp $sqlite $connectors $skill" ;;
	runner) echo "$mcp $pgx $sqlite $redis $connectors" ;;
	toolsets) echo "$anthropic $pgx $sqlite $redis $skill" ;;
	*) echo "check-deps: unknown variant $1" >&2; exit 2 ;;
	esac
}

status=0
for v in "$@"; do
	var="TAGS_$v"
	tags="${!var-}"
	deps="$(go list -tags "$tags" -deps ./cmd/agentkit)"
	for f in $(forbidden "$v"); do
		if found="$(grep -F "$f" <<<"$deps" | head -3)" && [ -n "$found" ]; then
			echo "agentkit-$v (-tags '$tags') links $f:" >&2
			sed 's/^/  /' <<<"$found" >&2
			status=1
		fi
	done
done
[ "$status" = 0 ] && echo "deps-check: $* link only what they should"
exit "$status"
