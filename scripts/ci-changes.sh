#!/usr/bin/env bash
# Says whether a change touches anything the image and smoke jobs check
# (check.yml's changes job, T008). A change that touches only docs/ and
# tasks/ cannot change an image or what the smoke suite runs, so those jobs
# skip it; anything else, or no list at all, runs them.
#
#   scripts/ci-changes.sh BASE HEAD   the paths git diff BASE HEAD names
#   scripts/ci-changes.sh -           the paths on stdin, one per line
#   scripts/ci-changes.sh --test      checks the rule, and that check.yml
#                                     wires it (make lint)
#
# Prints code=true or code=false (for $GITHUB_OUTPUT), and on stderr why.
# Any error (git cannot diff, no arguments) exits non-zero and prints
# nothing on stdout: the jobs read a missing answer as "run".
set -euo pipefail

# classify reads paths on stdin and prints code=false only if there is at
# least one and every one is under docs/ or tasks/.
classify() {
	local n=0 p
	while IFS= read -r p || [ -n "$p" ]; do
		[ -n "$p" ] || continue
		n=$((n + 1))
		case "$p" in
		docs/* | tasks/*) ;;
		*)
			echo "ci-changes: $p is outside docs/ and tasks/: run every job" >&2
			echo code=true
			return
			;;
		esac
	done
	if [ "$n" = 0 ]; then
		echo "ci-changes: no changed paths: run every job" >&2
		echo code=true
		return
	fi
	echo "ci-changes: all $n changed paths are under docs/ or tasks/: the image and smoke jobs skip" >&2
	echo code=false
}

# job NAME prints the lines of check.yml's job NAME.
job() {
	awk -v want="  $1:" '
		/^  [A-Za-z0-9_-]+:/ { injob = ($0 == want) }
		injob { print }
	' "$(dirname "$0")/../.github/workflows/check.yml"
}

if [ "${1:-}" = --test ]; then
	errors=0
	want() { # want WHAT GOT EXPECTED
		if [ "$2" != "$3" ]; then
			echo "ci-changes --test: $1: got ${2:-<nothing>}, want $3" >&2
			errors=$((errors + 1))
		fi
	}
	try() { printf '%s' "$1" | "$0" - 2>/dev/null; }
	want "docs only" "$(try $'docs/PLAN.md\n')" code=false
	want "tasks only" "$(try $'tasks/T008-ci-pipeline.md\n')" code=false
	want "docs and tasks" "$(try $'docs/SIZES.md\ntasks/E01-foundations.md\n')" code=false
	want "a Go file" "$(try $'docs/PLAN.md\npkg/store/store.go\n')" code=true
	want "a root file" "$(try $'tasks/STATUS.md\nAGENTS.md\n')" code=true
	want "the workflow" "$(try $'.github/workflows/check.yml\n')" code=true
	want "a docs/ prefix that is not the folder" "$(try $'docsite/index.html\n')" code=true
	want "web/ docs" "$(try $'web/docs/README.md\n')" code=true
	want "nothing" "$(try '')" code=true
	want "blank lines only" "$(try $'\n\n')" code=true
	if "$0" >/dev/null 2>&1 || "$0" no-such-ref HEAD >/dev/null 2>&1; then
		want "no arguments, or a ref git cannot diff" "exit 0" "a failure"
	fi
	# check.yml: the changes job runs this script; the image and smoke
	# jobs need it and skip only on an explicit false (a failed or
	# missing answer runs them); integration requires its variables.
	grep -q 'scripts/ci-changes.sh' <<<"$(job changes)" ||
		want "check.yml changes job" "no scripts/ci-changes.sh" "runs it"
	for j in image smoke; do
		body=$(job "$j")
		grep -qE '^    needs: \[?changes' <<<"$body" ||
			want "check.yml $j job" "no needs: changes" "needs: changes"
		grep -qF "if: \${{ !cancelled() && needs.changes.outputs.code != 'false' }}" <<<"$body" ||
			want "check.yml $j job" "another if" "if: !cancelled() && code != 'false'"
	done
	grep -qF 'AGENTKIT_REQUIRE_INTEGRATION: "1"' <<<"$(job integration)" ||
		want "check.yml integration job" "no AGENTKIT_REQUIRE_INTEGRATION" 'AGENTKIT_REQUIRE_INTEGRATION: "1"'
	if ((errors > 0)); then exit 1; fi
	echo "ci-changes --test: ok"
	exit 0
fi

case "${1:-}" in
-) classify ;;
"") echo "usage: scripts/ci-changes.sh BASE HEAD | - | --test" >&2; exit 2 ;;
*)
	[ $# = 2 ] || { echo "usage: scripts/ci-changes.sh BASE HEAD | - | --test" >&2; exit 2; }
	paths=$(git diff --name-only "$1" "$2")
	classify <<<"$paths"
	;;
esac
