#!/usr/bin/env bash
# Fail if total test coverage is below the floor. Usage: cover-check.sh [threshold]
set -euo pipefail
threshold="${1:-80}"
go test -race -coverprofile=cover.out ./... >/dev/null
pct="$(go tool cover -func=cover.out | awk '/^total:/{gsub(/%/,"",$3); print $3}')"
printf 'coverage: %s%% (floor %s%%)\n' "$pct" "$threshold"
awk -v p="$pct" -v t="$threshold" 'BEGIN{exit !(p+0 >= t+0)}' || {
  echo "FAIL: coverage ${pct}% is below the ${threshold}% floor" >&2
  exit 1
}
