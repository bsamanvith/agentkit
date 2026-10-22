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
# failed or never ended (make test TEST_JSON=file, which CI's make check uses).
#
#   scripts/test-timing.sh --test   checks both modes on the run in
#                                   scripts/testdata/test-timing (make lint)
#
# A package's time is its test binary's run (go test's Elapsed), not its
# build. A test's is wall time from when it last started or continued (a
# parallel test pauses until the serial ones are done) to its end, subtests
# included: go test's own Elapsed for a parallel test stops when it pauses.
# Parallel tests overlap, so they do not add up to their package's time.
set -euo pipefail

top=${TOP:-10}
mode=timing
if [[ ${1:-} == --test ]]; then
	here=$(cd "$(dirname "$0")" && pwd)
	fixture=$here/testdata/test-timing/run.json
	errors=0
	check() { # check WHAT OUTPUT GREP-PATTERN WANT(yes|no)
		if printf '%s\n' "$2" | grep -qE -- "$3"; then got=yes; else got=no; fi
		if [[ $got != "$4" ]]; then
			echo "test-timing --test: $1: want $3 present=$4" >&2
			errors=$((errors + 1))
		fi
	}
	out=$("$0" --output "$fixture")
	check output "$out" 'HIDDEN' no
	check output "$out" 'SHOWN the assertion of a failed test' yes
	check output "$out" 'SHOWN test timed out' yes
	check output "$out" 'SHOWN boom in another goroutine' yes
	check output "$out" '^=== RUN' no
	for result in 'ok  	ex/passed' 'FAIL	ex/failed' 'FAIL	ex/timedout' 'FAIL	ex/panicked'; do
		check output "$out" "^$result" yes
	done
	out=$("$0" "$fixture")
	check timing "$out" '^ +3\.1  ex/timedout TestHangs \(unfinished\)$' yes
	check timing "$out" '^ +0\.5  ex/failed TestBroken \(fail\)$' yes
	check timing "$out" '^ +1  ex/passed TestQuiet$' yes
	check timing "$out" '^Tests: 4 top-level, 4 with subtests, 1 failed, 2 unfinished\.$' yes
	if ((errors > 0)); then exit 1; fi
	echo "test-timing --test: ok"
	exit 0
fi
if [[ ${1:-} == --output ]]; then
	mode=output
	shift
fi
in=${1:--}

command -v jq >/dev/null || { echo "test-timing: needs jq" >&2; exit 2; }

if [[ $mode == output ]]; then
	# A test's output is hidden only when the test passed or was skipped: a
	# test that failed, or never ended because its binary died under it (a
	# timeout, a panic in another goroutine, os.Exit), shows its output, as
	# go test without -json shows the panic and its stack.
	jq -rjs '
		(map(select((.Action == "pass" or .Action == "skip") and .Test != null) | {key: "\(.Package) \(.Test)", value: true}) | from_entries) as $ok
		| (map(select(.Action == "fail" and .Test == null) | {key: .Package, value: true}) | from_entries) as $failedPkgs
		| .[]
		| if .Action == "build-output" then .Output
		elif .Action != "output" then empty
		# A package that passed shows only its result line, as without -json.
		elif .Test == null then (if $failedPkgs[.Package] or (.Output | test("^(ok|\\?) ")) then .Output else empty end)
		elif ($ok["\(.Package) \(.Test)"] | not) and (.Output | test("^=== (RUN|PAUSE|CONT|NAME)") | not) then .Output
		else empty end
	' "$in"
	exit 0
fi

jq -rs --argjson n "$top" '
	# RFC 3339 to seconds. The zone offset (go test writes local time) is
	# dropped, as only differences within one run are used, and the fraction
	# is optional: Go omits it when it is zero.
	def secs: capture("^(?<s>[0-9-]+T[0-9:]+)(\\.(?<f>[0-9]+))?") | (.s + "Z" | fromdateiso8601) + ("0." + (.f // "0") | tonumber);
	def final: .a == "pass" or .a == "fail" or .a == "skip";
	def row: "  \(.e * 100 | round / 100 | tostring | (" " * ([8 - length, 0] | max)) + .)  \(.p)\(if .t != "" then " " + .t else "" end)\(if .a != "pass" then " (" + .a + ")" else "" end)";
	[.[] | select(.Action == "run" or .Action == "cont" or .Action == "pass" or .Action == "fail" or .Action == "skip")
		| {p: .Package, t: (.Test // ""), a: .Action, at: (.Time | secs), el: .Elapsed}] as $ev
	| ($ev | map(select(.t == "" and final)) | map(.e = .el)) as $pkgs
	| ($pkgs | map({key: .p, value: .at}) | from_entries) as $pkgEnd
	# A test with no end of its own was running when its binary died (a
	# timeout, a panic): it ran until its package ended, and shows as
	# "unfinished", since it is the one to look at.
	| ($ev | map(select(.t != "")) | group_by([.p, .t])
		| map((map(select(.a == "run" or .a == "cont")) | last) as $run | (map(select(final)) | last) as $end
			| (if $end != null then $end
				elif $run != null and $pkgEnd[$run.p] != null then $run + {a: "unfinished", at: $pkgEnd[$run.p], el: null}
				else null end) as $end
			| select($end != null)
			| $end + {e: (if $run != null then $end.at - $run.at else $end.el end)})) as $tests
	| ($tests | map(select(.t | contains("/") | not))) as $toplevel
	| "Slowest \($n) packages (seconds, the test binary'"'"'s run):",
		($pkgs | sort_by(-.e) | .[:$n][] | row),
		"",
		"Slowest \($n) top-level tests (seconds, wall):",
		($toplevel | sort_by(-.e) | .[:$n][] | row),
		"",
		"Tests: \($toplevel | length) top-level, \($tests | length) with subtests, \($tests | map(select(.a == "fail")) | length) failed, \($tests | map(select(.a == "unfinished")) | length) unfinished."
' "$in"
