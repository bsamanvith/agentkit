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
# The A2A server (T067): only the images that run the agent API serve it.
a2a='github.com/bsamanvith/agentkit/pkg/a2a'
# The AG-UI server (T069): likewise.
agui='github.com/bsamanvith/agentkit/pkg/agui'
webhook='github.com/bsamanvith/agentkit/pkg/webhook'
openai='github.com/bsamanvith/agentkit/pkg/llm/openaicompat'
# The credential vault's SQL backends (T043), named on their own so that a
# backend wired without its store's build tag fails here even before it
# pulls in the driver.
credsqlite='github.com/bsamanvith/agentkit/pkg/credentials/sqlite'
credpg='github.com/bsamanvith/agentkit/pkg/credentials/postgres'
# What only the agent component (the agent API) uses, which the noagent tag
# leaves out: the store backends and their opener, the bus opener, the
# agents' memory and the MCP servers added at run time. The agent API's
# package itself stays imported (its names and wire types are shared), but
# its server is not linked.
stores='github.com/bsamanvith/agentkit/pkg/store/'
busopen='github.com/bsamanvith/agentkit/pkg/bus/busopen'
memory='github.com/bsamanvith/agentkit/pkg/memory'
servers='github.com/bsamanvith/agentkit/pkg/mcp/servers'
# Connecting accounts (T045): the agent API's credential endpoints.
connect='github.com/bsamanvith/agentkit/pkg/credentials/connect'
agent="$stores $busopen $memory $servers $connect"

# What each variant must not link.
forbidden() {
	case "$1" in
	local) echo "$pgx $redis $credpg" ;;
	dev) echo "$sqlite $credsqlite $agui" ;;
	api) echo "$anthropic $openai $mcp $sqlite $connectors $skill $agui $credsqlite" ;;
	runner) echo "$mcp $pgx $sqlite $redis $connectors $a2a $agui $webhook $credsqlite $credpg $agent" ;;
	toolsets) echo "$anthropic $openai $pgx $sqlite $redis $skill $a2a $agui $webhook $credsqlite $credpg $agent" ;;
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
