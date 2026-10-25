#!/usr/bin/env bash
# Fails when total statement coverage drops below COVER_MIN (default 80).
#
# Coverage is measured across every package at once (-coverpkg), so code
# exercised only by another package's tests (e.g. cmd/gateway end-to-end
# tests reaching internal/mitm) counts. Developer tools that need live
# accounts or a second gateway (cmd/livecheck, cmd/shadowcompare,
# cmd/speccheck) and the test helper package are left out of the measure.
#
#   scripts/coverage_gate.sh                # gate at 80%
#   COVER_MIN=85 scripts/coverage_gate.sh   # stricter
set -euo pipefail

min="${COVER_MIN:-80}"
profile="${COVER_PROFILE:-cover.out}"
pkgs="$(go list ./... | grep -vE 'cmd/(livecheck|shadowcompare|speccheck)|internal/testutil' | paste -sd, -)"

go test -count=1 -coverpkg="$pkgs" -coverprofile="$profile" ./...

total="$(go tool cover -func="$profile" | awk '/^total:/ {gsub("%", "", $NF); print $NF}')"
echo "total statement coverage: ${total}% (minimum ${min}%)"
if ! awk -v t="$total" -v m="$min" 'BEGIN { exit !(t + 0 >= m + 0) }'; then
  echo "coverage ${total}% is below the ${min}% minimum" >&2
  exit 1
fi
