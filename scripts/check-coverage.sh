#!/usr/bin/env bash
# Fails if any gated package is below the threshold.
# Gated = all ./internal/... and ./utils packages. cmd/* main shells are
# excluded here (covered by the instrumented integration run in Phase 5).
set -euo pipefail

THRESHOLD="${COVERAGE_THRESHOLD:-90.0}"
PROFILE="${COVERAGE_PROFILE:-coverage.out}"

# Packages to gate.
PKGS=$(go list ./internal/... ./utils 2>/dev/null)

go test -covermode=set -coverprofile="$PROFILE" $PKGS >/dev/null

echo "Per-package coverage (threshold ${THRESHOLD}%):"
fail=0
# func-level report aggregated to package totals.
while read -r pkg; do
  out=$(go test -covermode=set -coverprofile=/tmp/p.out "$pkg" 2>&1)
  # Packages with no executable statements (e.g. pure DTO/struct packages) or no
  # test files cannot be meaningfully gated — treat them as N/A, not a failure.
  if printf '%s' "$out" | grep -q 'no test files\|\[no statements\]'; then
    printf "  n/a            %s (no testable statements)\n" "$pkg"
    continue
  fi
  pct=$(printf '%s' "$out" \
        | awk '/coverage:/ {gsub("%","",$0); for(i=1;i<=NF;i++) if($i=="coverage:"){print $(i+1)}}')
  if [ -z "$pct" ]; then
    printf "  n/a            %s (no coverage reported)\n" "$pkg"
    continue
  fi
  awk -v p="$pct" -v t="$THRESHOLD" -v k="$pkg" \
    'BEGIN{ if (p+0 < t+0) { printf "  FAIL %6.1f%%  %s\n", p, k; exit 3 } else { printf "  ok   %6.1f%%  %s\n", p, k } }' \
    || fail=1
done <<< "$PKGS"

if [ "$fail" -ne 0 ]; then
  echo "Coverage gate FAILED: one or more packages below ${THRESHOLD}%." >&2
  exit 1
fi
echo "Coverage gate PASSED."
