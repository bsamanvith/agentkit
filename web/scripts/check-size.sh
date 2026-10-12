#!/bin/sh
# Fails if the console's first load is over its budget: the gzipped scripts
# and styles in pkg/web/dist/assets, as pkg/web sends them (fonts load only
# when used, and are cached). It runs on slow links and low-end machines, so
# raising the budget is a decision, like the image's (make image-size).
# Run from the repository root (make web-check does).
set -eu
budget=${WEB_BUDGET_KB:-200}
total=0
for f in pkg/web/dist/index.html pkg/web/dist/assets/*.js pkg/web/dist/assets/*.css; do
	n=$(gzip -9 -c "$f" | wc -c)
	total=$((total + n))
done
echo "console first load: $total bytes gzipped, budget $((budget * 1000)) (${budget} KB)"
if [ "$total" -gt $((budget * 1000)) ]; then
	echo "the console is over its size budget: lazy-load what the first screen doesn't need (React.lazy), or drop a dependency" >&2
	exit 1
fi
