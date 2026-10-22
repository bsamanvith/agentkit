#!/usr/bin/env bash
# Runs `go test "$@"` and, when it fails in GitHub Actions, turns each
# failing test into an ::error annotation with the lines it logged.
#
# The integration job's log ends with the service containers' output, and
# the API only serves a log's last lines, so a failing test's name can be
# lost from it. Annotations stay on the check run, whatever the log's size.
#
#   scripts/go-test-annotate.sh -race ./pkg/x/...
#   scripts/go-test-annotate.sh --test   checks the annotations made from
#                                        scripts/testdata/go-test-annotate (make lint)
set -uo pipefail

annotate() { # annotate FILE: go test output in, ::error lines out
	# A "--- FAIL: TestX" line with what the test logged (the indented lines
	# after it, at most 40), a panic with its trace (at most 40 lines, up to the package result), or a package
	# that didn't build. At most 10 annotations.
	awk '
		function esc(s) { gsub(/%/, "%25", s); gsub(/\r/, "%0D", s); gsub(/\n/, "%0A", s); return s }
		function prop(s) { s = esc(s); gsub(/:/, "%3A", s); gsub(/,/, "%2C", s); return s }
		function flush() {
			if (title != "") printf "::error title=%s::%s\n", prop(title), esc(body)
			title = ""; body = ""
			if (n == 10) exit
		}
		title != "" && left > 0 && (panic || /^[ \t]/) && $0 !~ /^([ \t]*--- FAIL: |FAIL\t|ok  \t)/ { body = body $0 "\n"; left--; next }
		{ flush() }
		/^[ \t]*--- FAIL: / || /^panic: / || /\[build failed\]/ || /\[setup failed\]/ {
			title = $0; sub(/^[ \t]+/, "", title); body = $0 "\n"; left = 40; panic = /^panic: /; n++
		}
		END { flush() }
	' "$1"
}

if [[ ${1:-} == --test ]]; then
	out=$(annotate "$(cd "$(dirname "$0")" && pwd)/testdata/go-test-annotate/out.txt")
	errors=0
	for want in \
		'::error title=--- FAIL%3A TestBroken (0.00s)::--- FAIL: TestBroken (0.00s)%0A    x_test.go:4: got 1, want: 2%0A' \
		'::error title=--- FAIL%3A TestParent (0.00s)::--- FAIL: TestParent (0.00s)%0A' \
		'::error title=--- FAIL%3A TestParent/child (0.00s)::    --- FAIL: TestParent/child (0.00s)%0A        x_test.go:9: child broke%0A' \
		'::error title=panic%3A boom::panic: boom%0A%0Agoroutine 7 [running]:%0Aex/panicked.TestPanics(0xc0)%0A' \
		'::error title=FAIL	ex/nobuild [setup failed]::FAIL	ex/nobuild [setup failed]%0A'; do
		if ! grep -qxF -- "$want" <<<"$out"; then
			echo "go-test-annotate --test: missing $want" >&2
			errors=$((errors + 1))
		fi
	done
	if [[ $(wc -l <<<"$out") -ne 5 ]]; then
		echo "go-test-annotate --test: want 5 annotations, got:" >&2
		echo "$out" >&2
		errors=$((errors + 1))
	fi
	[[ $errors -eq 0 ]] || exit 1
	echo "go-test-annotate --test: ok"
	exit 0
fi

log=$(mktemp)
trap 'rm -f "$log"' EXIT
go test "$@" 2>&1 | tee "$log"
status=${PIPESTATUS[0]}
[ "$status" -eq 0 ] && exit 0
[ -n "${GITHUB_ACTIONS:-}" ] && annotate "$log"
exit "$status"
