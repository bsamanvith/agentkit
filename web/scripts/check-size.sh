#!/bin/sh
# Fails if the console's first load is over its budget. It runs on slow links
# and low-end machines, so raising the budget is a decision, like the image's
# (make image-size).
#
# The first load is what index.html loads (its scripts, styles and
# modulepreloads, gzipped as pkg/web sends them) plus the Latin fonts, which
# every page uses. Lazy chunks (React.lazy) and the Latin Extended fonts load
# only when needed, so they don't count. Run from the repository root (make
# web-check does).
set -eu
dist=pkg/web/dist
budget=${WEB_BUDGET_KB:-240}
[ -f "$dist/index.html" ] || { echo "no $dist/index.html: run make web" >&2; exit 1; }

refs=$(grep -oE '(src|href)="/[^"/][^"]*\.(js|css)"' "$dist/index.html" | sed -E 's/^(src|href)="\///; s/"$//')
[ -n "$refs" ] || { echo "$dist/index.html loads no script or style" >&2; exit 1; }
total=$(gzip -9 -c "$dist/index.html" | wc -c)
for f in $refs; do
	[ -f "$dist/$f" ] || { echo "index.html loads $f, which is not in $dist" >&2; exit 1; }
	total=$((total + $(gzip -9 -c "$dist/$f" | wc -c)))
done
fonts=0
for f in "$dist"/assets/*-latin-wght-*.woff2; do
	[ -f "$f" ] || continue
	fonts=$((fonts + $(wc -c < "$f")))
done
[ "$fonts" -gt 0 ] || { echo "no Latin fonts (*-latin-wght-*.woff2) in $dist/assets: did their names change?" >&2; exit 1; }
total=$((total + fonts))
echo "console first load: $total bytes ($fonts of fonts), budget $((budget * 1000)) (${budget} KB)"
if [ "$total" -gt $((budget * 1000)) ]; then
	echo "the console is over its size budget: lazy-load what the first screen doesn't need (React.lazy), or drop a dependency" >&2
	exit 1
fi
