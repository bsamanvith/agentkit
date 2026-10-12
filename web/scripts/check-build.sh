#!/bin/sh
# Rebuilds the console and fails if the build in pkg/web/dist was not what
# the sources make: a change to web/ needs `make web` and the new
# pkg/web/dist in the same commit, because the binary embeds what is
# committed. Run from the repository root (make web-check does).
set -eu
dist=pkg/web/dist
before=$(mktemp -d)
trap 'rm -rf "$before"' EXIT
cp -R "$dist/." "$before/"
pnpm --dir web build
if ! diff -r "$before" "$dist" >/dev/null; then
	echo "$dist is stale: the sources build something else. Run make web and commit $dist." >&2
	diff -rq "$before" "$dist" >&2 || true
	exit 1
fi
echo "$dist matches the sources"
