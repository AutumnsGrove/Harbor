#!/usr/bin/env bash
# Run every fuzz target once. `go test -fuzz` accepts exactly one target per
# invocation, so this enumerates packages and targets instead of pretending a
# loop belongs in the Makefile (docs/plans/testing.md §3).
#
# FUZZTIME overrides the per-target budget: 30s in a PR, 10m nightly.
set -euo pipefail
cd "$(dirname "$0")/.."

fuzztime="${FUZZTIME:-30s}"
found=0

while IFS= read -r pkg; do
	mapfile -t targets < <("${GO:-go}" test -list '^Fuzz' "$pkg" 2>/dev/null | grep '^Fuzz' || true)
	for target in "${targets[@]}"; do
		found=1
		echo "fuzz: ${pkg} ${target} (${fuzztime})"
		"${GO:-go}" test -run '^$' -fuzz "^${target}\$" -fuzztime "$fuzztime" "$pkg"
	done
done < <("${GO:-go}" list ./...)

if [ "$found" -eq 0 ]; then
	echo "fuzz: no targets found" >&2
	exit 1
fi
