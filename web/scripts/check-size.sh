#!/bin/sh
# Fails if an app's first load is over its budget: the console's by
# default, or WEB_APP's build in WEB_DIST with WEB_BUDGET_KB (make web-check
# checks the farm app's too). It runs on slow links
# and low-end machines, so raising the budget is a decision, like the image's
# (make image-size).
#
# The first load is what index.html loads (its scripts, styles and
# modulepreloads, gzipped as pkg/web sends them: the build holds them packed,
# name.gz, see make web) plus the Latin fonts, which
# every page uses. Lazy chunks (React.lazy) and the Latin Extended fonts load
# only when needed, so they don't count. Run from the repository root (make
# web-check does).
set -eu
dist=${WEB_DIST:-pkg/web/dist}
app=${WEB_APP:-console}
budget=${WEB_BUDGET_KB:-240}

# sent prints the bytes pkg/web sends for a file of the build: its packed
# form when it has one, else the file (too small to gain from gzip).
sent() {
	if [ -f "$dist/$1.gz" ]; then wc -c <"$dist/$1.gz"; else wc -c <"$dist/$1"; fi
}
# show prints a text file of the build, packed or not.
show() {
	if [ -f "$dist/$1.gz" ]; then gzip -dc "$dist/$1.gz"; else cat "$dist/$1"; fi
}
[ -f "$dist/index.html" ] || [ -f "$dist/index.html.gz" ] || { echo "no $dist/index.html: run make web" >&2; exit 1; }

refs=$(show index.html | grep -oE '(src|href)="/[^"/][^"]*\.(js|css)"' | sed -E 's/^(src|href)="\///; s/"$//')
[ -n "$refs" ] || { echo "$dist/index.html loads no script or style" >&2; exit 1; }
total=$(sent index.html)
for f in $refs; do
	[ -f "$dist/$f" ] || [ -f "$dist/$f.gz" ] || { echo "index.html loads $f, which is not in $dist" >&2; exit 1; }
	total=$((total + $(sent "$f")))
done
fonts=0
for f in "$dist"/assets/*-latin-wght-*.woff2; do
	[ -f "$f" ] || continue
	fonts=$((fonts + $(wc -c < "$f")))
done
[ "$fonts" -gt 0 ] || { echo "no Latin fonts (*-latin-wght-*.woff2) in $dist/assets: did their names change?" >&2; exit 1; }
total=$((total + fonts))
echo "$app first load: $total bytes ($fonts of fonts), budget $((budget * 1000)) (${budget} KB)"
if [ "$total" -gt $((budget * 1000)) ]; then
	echo "the $app is over its size budget: lazy-load what the first screen doesn't need (React.lazy), or drop a dependency" >&2
	exit 1
fi
