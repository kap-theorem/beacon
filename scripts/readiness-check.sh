#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# scripts/readiness-check.sh
#
# Beacon API readiness check — exercises every documented and undocumented
# endpoint and prints request + response body + HTTP status.
#
# Usage:
#   ./scripts/readiness-check.sh [BASE_URL]
#   BASE_URL defaults to http://127.0.0.1:6969
#
# Requires: curl
# ---------------------------------------------------------------------------

set -euo pipefail

BASE_URL="${1:-http://127.0.0.1:6969}"
ADMIN_TOKEN="${ADMIN_TOKEN:-devsecret}"
PASS=0
FAIL=0

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------
print_separator() {
  echo ""
  echo "================================================================"
}

run_check() {
  local label="$1"
  local method="$2"
  local path="$3"
  local expected_status="$4"
  shift 4
  local extra_args=("$@")

  print_separator
  echo "CHECK: ${label}"
  echo "  ${method} ${BASE_URL}${path}"

  # Capture status code and body separately
  local http_body
  local http_status
  http_body=$(curl -s -o /tmp/readiness_body.tmp -w "%{http_code}" \
    -X "${method}" "${BASE_URL}${path}" "${extra_args[@]}" 2>&1)
  http_status="${http_body}"
  http_body=$(cat /tmp/readiness_body.tmp 2>/dev/null || echo "")

  echo "  STATUS:   ${http_status}"
  echo "  BODY:     ${http_body}"

  if [ "${http_status}" = "${expected_status}" ]; then
    echo "  RESULT:   PASS"
    PASS=$((PASS + 1))
  else
    echo "  RESULT:   FAIL  (expected ${expected_status}, got ${http_status})"
    FAIL=$((FAIL + 1))
  fi
}

# ---------------------------------------------------------------------------
# Health checks
# ---------------------------------------------------------------------------
run_check "GET /healthz/live — expect 200" \
  GET /healthz/live "200"

run_check "GET /healthz/ready — expect 200" \
  GET /healthz/ready "200"

# ---------------------------------------------------------------------------
# Email notification
# ---------------------------------------------------------------------------
print_separator
echo "CHECK: POST /notify/email (valid) — expect 202"
echo "  POST ${BASE_URL}/notify/email"

NOTIFY_STATUS=$(curl -s -o /tmp/readiness_notify.tmp -w "%{http_code}" \
  -X POST "${BASE_URL}/notify/email" \
  -H "Content-Type: application/json" \
  -d '{"to":"alice@example.com","subject":"Readiness check","body":"hello from beacon"}')
NOTIFY_BODY=$(cat /tmp/readiness_notify.tmp)

echo "  STATUS:   ${NOTIFY_STATUS}"
echo "  BODY:     ${NOTIFY_BODY}"
WORKFLOW_ID=$(echo "${NOTIFY_BODY}" | grep -o '"workflow_id":"[^"]*"' | cut -d'"' -f4 || echo "")
echo "  workflow_id: ${WORKFLOW_ID}"

if [ "${NOTIFY_STATUS}" = "202" ]; then
  echo "  RESULT:   PASS"
  PASS=$((PASS + 1))
else
  echo "  RESULT:   FAIL  (expected 202, got ${NOTIFY_STATUS})"
  FAIL=$((FAIL + 1))
fi

run_check "POST /notify/email (invalid email) — expect 400" \
  POST /notify/email "400" \
  -H "Content-Type: application/json" \
  -d '{"to":"not-an-email","subject":"x","body":"y"}'

run_check "POST /notify/email (missing subject) — expect 400" \
  POST /notify/email "400" \
  -H "Content-Type: application/json" \
  -d '{"to":"a@b.com","body":"y"}'

run_check "GET /notify/email — expect 405 (method not allowed)" \
  GET /notify/email "405"

# ---------------------------------------------------------------------------
# Admin endpoints
# ---------------------------------------------------------------------------
run_check "POST /admin/config/refresh (no token) — expect 401" \
  POST /admin/config/refresh "401"

# In DEV_MODE, valid token returns 503 (ErrDevModeSkip)
run_check "POST /admin/config/refresh (Bearer devsecret, DEV_MODE) — expect 503" \
  POST /admin/config/refresh "503" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}"

# ---------------------------------------------------------------------------
# DLQ endpoints
# ---------------------------------------------------------------------------
run_check "GET /dlq/failed — expect 200 with count" \
  GET /dlq/failed "200"

run_check "GET /dlq/failed?from=bad-date — expect 400" \
  GET "/dlq/failed?from=bad-date" "400"

run_check "POST /dlq/replay/nonexistent-workflow-id — expect 404" \
  POST /dlq/replay/nonexistent-workflow-id "404"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
print_separator
echo ""
echo "READINESS CHECK SUMMARY"
echo "  Base URL:  ${BASE_URL}"
echo "  PASS:      ${PASS}"
echo "  FAIL:      ${FAIL}"
echo "  TOTAL:     $((PASS + FAIL))"
echo ""

if [ "${FAIL}" -eq 0 ]; then
  echo "  All checks passed."
  exit 0
else
  echo "  ${FAIL} check(s) failed."
  exit 1
fi
