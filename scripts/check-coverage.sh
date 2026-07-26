#!/usr/bin/env bash

set -euo pipefail

profile="${1:-}"
minimum="${2:-}"
label="${3:-Go coverage}"

if [[ -z "$profile" || -z "$minimum" ]]; then
  echo "usage: $0 COVERAGE_PROFILE MINIMUM_PERCENT [LABEL]" >&2
  exit 2
fi
if [[ ! -f "$profile" ]]; then
  echo "$label profile is missing: $profile" >&2
  exit 2
fi
if [[ ! "$minimum" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
  echo "$label minimum is not a percentage: $minimum" >&2
  exit 2
fi

coverage="$(
  go tool cover -func="$profile" |
    awk '/^total:/ {gsub("%", "", $3); print $3}'
)"
if [[ -z "$coverage" ]]; then
  echo "$label total could not be read from $profile" >&2
  exit 2
fi

awk -v coverage="$coverage" -v minimum="$minimum" -v label="$label" 'BEGIN {
  if (coverage + 0 < minimum + 0) {
    printf "%s %.1f%% is below %.1f%%\n", label, coverage, minimum > "/dev/stderr"
    exit 1
  }
  printf "%s %.1f%% (minimum %.1f%%)\n", label, coverage, minimum
}'
