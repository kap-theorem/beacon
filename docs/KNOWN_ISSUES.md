# Known Issues — Gaps and Bugs

Found during local API testing on 2026-06-04. Server running in `DEV_MODE=true` against a live Mailpit + Temporal stack.

---

## Bugs

### B1 — `POST /notify/email`: No email address validation

**File**: [internal/api/email.go:41](../internal/api/email.go#L41)

The handler only checks that `to` and `subject` are non-empty strings. It does not validate email format. Requests with a whitespace-only `to` (e.g. `"   "`) or a malformed address (e.g. `"not-an-email"`) are accepted with `202 Accepted` and trigger a Temporal workflow that immediately fails at the SMTP send step, landing in the DLQ.

**Observed**:
```bash
# Both return 202 — workflow created and immediately fails
curl -X POST localhost:6969/notify/email -d '{"to":"   ","subject":"hi"}'
curl -X POST localhost:6969/notify/email -d '{"to":"not-an-email","subject":"hi"}'
```

**Expected**: `400 Bad Request` with a clear error before any workflow is enqueued.

---

### B2 — `GET /dlq/failed?status=`: Case-sensitive filter silently returns zero results

**File**: [internal/dlq/query.go:197](../internal/dlq/query.go#L197)

`statusMatches` does an exact string comparison against `"Failed"`, `"TimedOut"`, and `"Canceled"`. Callers passing `status=failed` or `status=FAILED` receive `200 OK` with an empty list rather than an error, making it look like there are no failures when there are.

**Observed**:
```bash
curl "localhost:6969/dlq/failed?status=failed"
# → {"count":0,"failures":[]}   ← wrong, 3 failures exist

curl "localhost:6969/dlq/failed?status=Failed"
# → {"count":3,"failures":[...]}  ← correct
```

**Fix**: Use `strings.EqualFold(status, filterStatus)` in `statusMatches`.

---

### B3 — Mock Infisical server path mismatch

**Files**: [scripts/mock-infisical.go:33](../scripts/mock-infisical.go#L33), [internal/config/service.go:207](../internal/config/service.go#L207)

The mock server registers its handler at `/api/v1/secrets` but `ConfigService.fetchConfigs` calls `/api/v4/secrets`. Running the mock against the server results in a 404 on every config load attempt — the mock is currently non-functional for integration testing.

**Observed**:
```
# mock registers:
http.HandleFunc("/api/v1/secrets", handleSecrets)

# service calls:
GET /api/v4/secrets?projectId=...
```

**Fix**: Change the mock handler path to `/api/v4/secrets`.

---

## Gaps

### G1 — `POST /notify/email`: Validation error message is misleading when only one field is missing

**File**: [internal/api/email.go:41](../internal/api/email.go#L41)

The check `if request.To == "" || request.Subject == ""` always returns the message `"missing required fields: to, subject"` regardless of which field is actually absent. A caller missing only `subject` is told both fields are missing.

**Fix**: Separate the checks and return field-specific error messages.

---

### G2 — `GET /healthz/live` and `GET /healthz/ready`: No HTTP method enforcement

**File**: [internal/config/health.go:30](../internal/config/health.go#L30)

The health handlers accept any HTTP method (POST, PUT, DELETE, etc.) and return `200 OK`. Standard health check conventions expect only `GET` (or `HEAD`) to be accepted.

**Observed**:
```bash
curl -X POST localhost:6969/healthz/live   # → 200 OK
curl -X DELETE localhost:6969/healthz/live # → 200 OK
```

---

### G3 — `GET /dlq/failed?from=` / `?to=`: Invalid date strings are silently ignored

**File**: [internal/api/dlq.go:38](../internal/api/dlq.go#L38)

Unparseable `from` or `to` query parameters are silently dropped and the query runs without a date filter. Callers with a typo in a date string get back all results with no indication something went wrong.

**Observed**:
```bash
curl "localhost:6969/dlq/failed?from=not-a-date"
# → 200 OK with all results, same as no filter
```

**Fix**: Return `400 Bad Request` if a non-empty date parameter cannot be parsed as RFC3339.

---

### G4 — `POST /dlq/replay/`: No idempotency guard

**File**: [internal/dlq/replay.go:22](../internal/dlq/replay.go#L22)

A failed workflow can be replayed any number of times in quick succession — each call dispatches a new `replay-{id}-{timestamp}` workflow. There is no deduplication, lock, or check for an already-running replay of the same original workflow.

**Observed**:
```bash
# Both return 202 and create separate new workflows
curl -X POST localhost:6969/dlq/replay/email-workflow-foo-123
curl -X POST localhost:6969/dlq/replay/email-workflow-foo-123
```

**Implication**: A retry storm or double-click from a UI could flood the queue with duplicate sends.

---

### G5 — `POST /admin/config/refresh`: No-op in `DEV_MODE`

**File**: [internal/config/service.go:313](../internal/config/service.go#L313)

When `DEV_MODE=true`, `RefreshConfig` returns immediately without calling Infisical. The endpoint always responds `200 OK` with `revision: 1`, making it impossible to test config refresh behavior (category routing, multi-provider switching, retry logic) while `DEV_MODE` is active.

**Implication**: To test the Infisical code path locally you must set `DEV_MODE=false` and point at a real or mock Infisical server. See [docs/infisical/QUICK_TEST.md](infisical/QUICK_TEST.md) for setup, and fix B3 first.
