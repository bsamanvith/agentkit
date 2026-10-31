#!/bin/sh
# Rebuilds each app embedded in a Go binary and fails if its committed build
# is not what the sources make: a change to web/ needs `make web` (the
# console, pkg/web/dist) or `make farm-web` (the farm app,
# apps/farm/webdist/dist) and the new build in the same commit, because the
# binaries embed what is committed. The build is Vite's, then pkg/web's pack
# step, which gzips its text files. Run from the repository root (make
# web-check does).
set -eu

# check app dist rebuilds the pnpm package app into dist and compares.
check() {
	app=$1 dist=$2
	before=$(mktemp -d)
	cp -R "$dist/." "$before/"
	if ! pnpm --dir web --filter "$app" build || ! go run ./pkg/web/pack.go "$dist"; then
		# Vite empties dist before building: put the committed build back.
		rm -rf "$dist" && mkdir -p "$dist" && cp -R "$before/." "$dist/"
		rm -rf "$before"
		echo "the $app build failed; $dist is as it was" >&2
		exit 1
	fi
	if ! diff -r "$before" "$dist" >/dev/null; then
		echo "$dist is stale: the sources build something else. Rebuild it (make web, make farm-web) and commit $dist." >&2
		diff -rq "$before" "$dist" >&2 || true
		rm -rf "$before"
		exit 1
	fi
	rm -rf "$before"
	echo "$dist matches the sources"
}

check @agentkit/console pkg/web/dist
check @agentkit/farm apps/farm/webdist/dist
