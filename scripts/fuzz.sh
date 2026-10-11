#!/usr/bin/env bash
# Runs every Fuzz* target in the module for the given duration (default 10s).
# `go test -fuzz` accepts one package and one target at a time, hence the loop.
set -euo pipefail

fuzztime="${1:-10s}"
cd "$(dirname "$0")/.."

found=0
while IFS= read -r file; do
  dir="./$(dirname "$file")"
  for target in $(grep -oE '^func (Fuzz[A-Za-z0-9_]+)' "$file" | awk '{print $2}'); do
    found=1
    echo "==> $dir $target ($fuzztime)"
    go test -run='^$' -fuzz="^${target}\$" -fuzztime="$fuzztime" "$dir"
  done
done < <(grep -rlE --include='*_test.go' '^func Fuzz' . | sed 's|^\./||' | sort)

if [[ "$found" == 0 ]]; then
  echo "no fuzz targets found"
fi
