#!/usr/bin/env bash
# The perf report (T034, docs/ENGINEERING.md 2.7): runs every Benchmark* in
# the repository COUNT times and compares the numbers with a previous run's
# using benchstat. .github/workflows/nightly.yml runs it each night, with
# the previous night's artifact as BASELINE. Report only: a slower number
# never fails it, until budgets are set in ENGINEERING 2.7.
#
#   scripts/perf-report.sh                     run, report in bin/perf/
#   BASELINE=old/bench.txt scripts/perf-report.sh   ... compared with old
#   scripts/perf-report.sh --test              checks the script on stubs,
#                                              and that nightly.yml wires it
#                                              (make lint)
#
# Writes to OUT (default bin/perf):
#   bench.txt      go test's raw output: the next run's BASELINE
#   benchstat.txt  benchstat's table: BASELINE against this run, or this
#                  run alone when there is no baseline
#   summary.md     the job summary: the table and what it compared
#
# Settings (environment):
#   COUNT=6            runs of each benchmark (benchstat wants 6 or more)
#   BENCH=.            -bench regexp; BENCHTIME=1s -benchtime
#   PKGS               packages to run (default: every one with a Benchmark*)
#   BASELINE           a previous bench.txt; missing or empty means none
#   BASELINE_LABEL     what BASELINE is, for the summary (a run, a commit)
#   GO=go, BENCHSTAT   the commands (BENCHSTAT defaults to benchstat at
#                      BENCHSTAT_VERSION, through go run)
#
# Exits non-zero only when a benchmark fails to run (a build error, a panic,
# a b.Fatal) or benchstat cannot read the numbers, after writing summary.md.
# Benchmarks run one package at a time (-p 1), so packages do not share the
# CPU while they are measured.
set -euo pipefail

BENCHSTAT_VERSION=v0.0.0-20250813145418-2f7363a06fe1

if [[ ${1:-} == --test ]]; then
	here=$(cd "$(dirname "$0")" && pwd)
	fixtures=$here/testdata/perf-report
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	errors=0
	fail() { echo "perf-report --test: $*" >&2; errors=$((errors + 1)); }
	has() { # has CASE FILE PATTERN: FILE matches the extended regexp
		grep -qE -- "$3" "$2" || fail "$1: $(basename "$2") lacks /$3/"
	}
	lacks() {
		! grep -qE -- "$3" "$2" || fail "$1: $(basename "$2") has /$3/"
	}
	# The stubs: go prints a recorded run and exits STUB_GO_STATUS, recording
	# its arguments; benchstat records its arguments and prints a table.
	cat >"$tmp/go" <<-'EOF'
		#!/usr/bin/env bash
		[ "$1" != env ] || { echo go-stub; exit 0; }
		printf '%s\n' "$*" >"$STUB_DIR/go.args"
		cat "$STUB_GO_OUTPUT"
		exit "${STUB_GO_STATUS:-0}"
	EOF
	cat >"$tmp/benchstat" <<-'EOF'
		#!/usr/bin/env bash
		printf '%s\n' "$*" >"$STUB_DIR/benchstat.args"
		[ -z "${STUB_BENCHSTAT_FAIL:-}" ] || { echo "benchstat: stub failure" >&2; exit 1; }
		echo "STUB-TABLE BenchmarkCallTool-4 2.06ms +50.00% (p=0.002 n=6)"
	EOF
	chmod +x "$tmp/go" "$tmp/benchstat"
	# run CASE [VAR=value...]: the script on the stubs, in a fresh OUT.
	run() {
		local name=$1
		shift
		rm -rf "$tmp/out" "$tmp"/*.args
		status=0
		env OUT="$tmp/out" GO="$tmp/go" BENCHSTAT="$tmp/benchstat" STUB_DIR="$tmp" \
			STUB_GO_OUTPUT="$fixtures/new.txt" PKGS="./pkg/a ./pkg/b" BASELINE= COUNT=3 "$@" \
			"$0" >"$tmp/$name.log" 2>&1 || status=$?
	}

	# 1. No baseline: this run's numbers alone, said plainly; exit 0.
	run first
	[[ $status == 0 ]] || fail "first: exit $status, want 0"
	cmp -s "$fixtures/new.txt" "$tmp/out/bench.txt" || fail "first: bench.txt is not go test's output"
	has first "$tmp/go.args" '^test -run \^\$ -bench \. -benchmem -benchtime 1s -count 3 -p 1 -timeout [0-9]+m ./pkg/a ./pkg/b$'
	has first "$tmp/benchstat.args" "^tonight=$tmp/out/bench.txt$"
	has first "$tmp/out/summary.md" '^## Perf report'
	has first "$tmp/out/summary.md" 'No baseline'
	has first "$tmp/out/summary.md" 'STUB-TABLE'
	has first "$tmp/out/benchstat.txt" 'STUB-TABLE'
	lacks first "$tmp/out/summary.md" 'failed'

	# 2. A baseline: benchstat compares it with this run, labelled; a slower
	# number (the stub's +50%) still exits 0: report only.
	run compare BASELINE="$fixtures/old.txt" BASELINE_LABEL="run 41 (abc1234)"
	[[ $status == 0 ]] || fail "compare: exit $status, want 0 (report only)"
	has compare "$tmp/benchstat.args" "^previous=$fixtures/old.txt tonight=$tmp/out/bench.txt$"
	has compare "$tmp/out/summary.md" 'Compared with run 41 \(abc1234\)'
	has compare "$tmp/out/summary.md" 'STUB-TABLE.*\+50\.00%'
	lacks compare "$tmp/out/summary.md" 'No baseline'

	# 3. A baseline that is missing or empty (an expired artifact) is none.
	: >"$tmp/empty.txt"
	for b in "$tmp/missing.txt" "$tmp/empty.txt"; do
		run none BASELINE="$b"
		[[ $status == 0 ]] || fail "none ($b): exit $status, want 0"
		has none "$tmp/benchstat.args" "^tonight=$tmp/out/bench.txt$"
		has none "$tmp/out/summary.md" 'No baseline'
	done

	# 4. A benchmark that fails: the summary says so and shows the failure,
	# the numbers that ran are still reported, and the exit is non-zero.
	run broken STUB_GO_OUTPUT="$fixtures/failed.txt" STUB_GO_STATUS=1 BASELINE="$fixtures/old.txt"
	[[ $status != 0 ]] || fail "broken: exit 0, want non-zero"
	has broken "$tmp/out/summary.md" 'Benchmarks failed'
	has broken "$tmp/out/summary.md" 'panic: boom in BenchmarkBroken'
	has broken "$tmp/out/summary.md" 'STUB-TABLE'

	# 5. benchstat that fails: said, and non-zero.
	run nostat STUB_BENCHSTAT_FAIL=1
	[[ $status != 0 ]] || fail "nostat: exit 0, want non-zero"
	has nostat "$tmp/out/summary.md" 'benchstat failed'
	has nostat "$tmp/out/summary.md" 'stub failure'

	# 6. The default package list: every package with a Benchmark*, and no
	# other (runs go list's way, from the repository root).
	pkgs=$(cd "$here/.." && "$0" --list-packages)
	grep -qx './pkg/mcp/server' <<<"$pkgs" || fail "packages: ./pkg/mcp/server (BenchmarkCallTool) is missing: $pkgs"
	! grep -qx './pkg/agentapi' <<<"$pkgs" || fail "packages: ./pkg/agentapi has no Benchmark* but is listed"

	# 7. nightly.yml wires it: schedule and manual dispatch, this script, the
	# job summary and the artifact the next night reads; and it is fork-safe:
	# no repository secret, no write permission.
	wf=$here/../.github/workflows/nightly.yml
	has workflow "$wf" '^  schedule:'
	has workflow "$wf" '^  workflow_dispatch:'
	has workflow "$wf" 'run: scripts/perf-report\.sh$'
	has workflow "$wf" 'GITHUB_STEP_SUMMARY'
	has workflow "$wf" 'name: perf$'
	has workflow "$wf" 'gh run download .*-n perf'
	lacks workflow "$wf" 'secrets\.'
	lacks workflow "$wf" ': *write'

	if ((errors > 0)); then exit 1; fi
	echo "perf-report --test: ok"
	exit 0
fi

# Every package with a Benchmark* in a test file, as ./dir.
list_packages() {
	git grep -l -E '^func Benchmark' -- '*_test.go' | xargs -r -n1 dirname | sort -u | sed 's|^|./|'
}
if [[ ${1:-} == --list-packages ]]; then
	list_packages
	exit 0
fi

out=${OUT:-bin/perf}
count=${COUNT:-6}
bench=${BENCH:-.}
benchtime=${BENCHTIME:-1s}
baseline=${BASELINE:-}
label=${BASELINE_LABEL:-$baseline}
gocmd=${GO:-go}
pkgs=${PKGS:-$(list_packages | tr '\n' ' ')}
if [[ -n ${BENCHSTAT:-} ]]; then
	read -r -a benchstat <<<"$BENCHSTAT"
else
	benchstat=("$gocmd" run "golang.org/x/perf/cmd/benchstat@$BENCHSTAT_VERSION")
fi
[[ -n $pkgs ]] || { echo "perf-report: no package has a Benchmark*" >&2; exit 2; }

mkdir -p "$out"
summary=$out/summary.md
status=0

echo "perf-report: go test -bench '$bench' -count $count on $pkgs" >&2
# shellcheck disable=SC2086 # pkgs is a list of package paths
"$gocmd" test -run '^$' -bench "$bench" -benchmem -benchtime "$benchtime" -count "$count" -p 1 -timeout 120m $pkgs \
	>"$out/bench.txt" 2>&1 || status=$?

if [[ -n $baseline && -s $baseline ]]; then
	args=("previous=$baseline" "tonight=$out/bench.txt")
	compared="Compared with $label (\`previous\`) by benchstat; \`tonight\` is this run."
else
	args=("tonight=$out/bench.txt")
	compared="No baseline${baseline:+ (\`$baseline\` is missing or empty)}: this run's numbers alone. The next run compares with them."
fi
stat_status=0
"${benchstat[@]}" "${args[@]}" >"$out/benchstat.txt" 2>&1 || stat_status=$?

{
	echo "## Perf report"
	echo
	echo "Commit \`$(git rev-parse --short HEAD 2>/dev/null || echo unknown)\`, $("$gocmd" env GOVERSION 2>/dev/null || echo "go unknown"), \`-count $count -benchtime $benchtime\`."
	echo "$compared"
	echo "Report only: nothing here fails the job; budgets come after two weeks of numbers (docs/ENGINEERING.md 2.7)."
	echo
	if ((status != 0)); then
		echo "**Benchmarks failed** (go test exit $status). What failed:"
		echo
		echo '```text'
		grep -E -A12 '(^--- FAIL|^FAIL|panic: |^# )' "$out/bench.txt" | head -60 || true
		echo '```'
		echo
	fi
	if ((stat_status != 0)); then
		echo "**benchstat failed** (exit $stat_status):"
	fi
	echo '```text'
	cat "$out/benchstat.txt"
	echo '```'
} >"$summary"

echo "perf-report: wrote $summary" >&2
if ((status != 0)); then
	echo "perf-report: benchmarks failed (go test exit $status); see $summary" >&2
	exit "$status"
fi
if ((stat_status != 0)); then
	echo "perf-report: benchstat failed (exit $stat_status); see $summary" >&2
	exit "$stat_status"
fi
