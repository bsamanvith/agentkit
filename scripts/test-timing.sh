#!/usr/bin/env bash
# Prints the slowest packages and top-level tests of a `go test -json` run
# (T056). Reads the JSON from the file named by its last argument, or from
# stdin; TOP (default 10) is how many of each to print.
#
#   go test -json -race -shuffle=on ./... > test.json; scripts/test-timing.sh test.json
#   make test-timing            # runs the unit tests, then prints this
#
# With --output it prints instead what go test without -json would have:
# build errors, each package's result line, and the output of the tests that
# failed (make test TEST_JSON=file, which CI's make check uses).
#
# A package's time is its test binary's run (go test's Elapsed), not its
# build. A test's is wall time from when it last started or continued (a
# parallel test pauses until the serial ones are done) to its end, subtests
# included: go test's own Elapsed for a parallel test stops when it pauses.
# Parallel tests overlap, so they do not add up to their package's time.
set -euo pipefail

top=${TOP:-10}
mode=timing
if [[ ${1:-} == --output ]]; then
	mode=output
	shift
fi
in=${1:--}

command -v jq >/dev/null || { echo "test-timing: needs jq" >&2; exit 2; }

if [[ $mode == output ]]; then
	jq -rjs '
		(map(select(.Action == "fail" and .Test != null) | [.Package, .Test]) | unique) as $failed
		| (map(select(.Action == "fail" and .Test == null) | .Package) | unique) as $failedPkgs
		| .[]
		| if .Action == "build-output" then .Output
		elif .Action != "output" then empty
		# A package that passed shows only its result line, as without -json.
		elif .Test == null then (if (.Package | IN($failedPkgs[])) or (.Output | test("^(ok|\\?) ")) then .Output else empty end)
		elif ([.Package, .Test] | IN($failed[])) and (.Output | test("^=== (RUN|PAUSE|CONT|NAME)") | not) then .Output
		else empty end
	' "$in"
	exit 0
fi

jq -rs --argjson n "$top" '
	# RFC 3339 with nanoseconds to Unix seconds.
	def secs: capture("^(?<s>[^.Z]+)(\\.(?<f>[0-9]+))?") | (.s + "Z" | fromdateiso8601) + ("0." + (.f // "0") | tonumber);
	def final: .a == "pass" or .a == "fail" or .a == "skip";
	def row: "  \(.e * 100 | round / 100 | tostring | (" " * ([8 - length, 0] | max)) + .)  \(.p)\(if .t != "" then " " + .t else "" end)\(if .a != "pass" then " (" + .a + ")" else "" end)";
	[.[] | select(.Action == "run" or .Action == "cont" or .Action == "pass" or .Action == "fail" or .Action == "skip")
		| {p: .Package, t: (.Test // ""), a: .Action, at: (.Time | secs), el: .Elapsed}] as $ev
	| ($ev | map(select(.t == "" and final)) | map(.e = .el)) as $pkgs
	| ($ev | map(select(.t != "")) | group_by([.p, .t])
		| map((map(select(.a == "run" or .a == "cont")) | last) as $run | (map(select(final)) | last) as $end
			| select($end != null)
			| $end + {e: (if $run != null then $end.at - $run.at else $end.el end)})) as $tests
	| ($tests | map(select(.t | contains("/") | not))) as $toplevel
	| "Slowest \($n) packages (seconds, the test binary'"'"'s run):",
		($pkgs | sort_by(-.e) | .[:$n][] | row),
		"",
		"Slowest \($n) top-level tests (seconds, wall):",
		($toplevel | sort_by(-.e) | .[:$n][] | row),
		"",
		"Tests: \($toplevel | length) top-level, \($tests | length) with subtests, \($tests | map(select(.a == "fail")) | length) failed."
' "$in"
