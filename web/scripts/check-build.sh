#!/bin/sh
# Rebuilds the console and fails if the build in pkg/web/dist was not what
# the sources make: a change to web/ needs `make web` and the new
# pkg/web/dist in the same commit, because the binary embeds what is
# committed. The build is Vite's, then pkg/web's pack step, which gzips its
# text files (make web runs both). Run from the repository root (make
# web-check does).
set -eu
dist=pkg/web/dist
before=$(mktemp -d)
trap 'rm -rf "$before"' EXIT
cp -R "$dist/." "$before/"
if ! pnpm --dir web build || ! go run ./pkg/web/pack.go "$dist"; then
	# Vite empties dist before building: put the committed build back.
	rm -rf "$dist" && mkdir -p "$dist" && cp -R "$before/." "$dist/"
	echo "the console build failed; $dist is as it was" >&2
	exit 1
fi
if ! diff -r "$before" "$dist" >/dev/null; then
	echo "$dist is stale: the sources build something else. Run make web and commit $dist." >&2
	diff -rq "$before" "$dist" >&2 || true
	exit 1
fi
echo "$dist matches the sources"
