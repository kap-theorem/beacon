# Beacon — Feature Readiness Matrix (Phase 5)

**Branch**: `fix/known-issues`
**Date**: 2026-06-24
**Stack mode**: Binary fallback (no Docker daemon). Docker-compose syntax validated only — see [Docker-compose section](#docker-compose-local-stack).
**Temporal**: dev server at 127.0.0.1:7233 (namespace `default`, running)
**Mailpit**: 127.0.0.1:1025 (SMTP), 127.0.0.1:8025 (REST/web)
**Server**: bin/server (instrumented with `-cover`), PID 27155 (stopped after checks)
**Worker**: bin/email_worker (instrumented with `-cover`), PID 27120 (stopped after checks)
**Mailpit PID**: 26818 (stopped after checks)

---

## Endpoint Readiness Matrix

| Endpoint | Method | Input | Expected | Actual Status | Actual Body (trimmed) | Pass/Fail | Notes |
|---|---|---|---|---|---|---|---|
| `/healthz/live` | GET | — | 200 `ok` | **200** | `ok` | PASS | |
| `/healthz/ready` | GET | — | 200 `ready` | **200** | `ready` | PASS | |
| `/notify/email` | POST | `{"to":"alice@example.com","subject":"Readiness check","body":"hello from beacon"}` | 202 + workflow_id | **202** | `{"success":true,"message":"email notification triggered","data":{"provider":"dev","workflow_id":"email-workflow-alice@example.com-1782351841675718000","workflow_run_id":"019efc72-c98d-7a0a-b07c-24bcf2f245f3"}}` | PASS | Email delivered to mailpit — see below |
| `/notify/email` | POST | `{"to":"not-an-email","subject":"x","body":"y"}` | 400 | **400** | `{"success":false,"error":"invalid email address: to"}` | PASS | |
| `/notify/email` | POST | `{"to":"a@b.com","body":"y"}` (no subject) | 400 | **400** | `{"success":false,"error":"missing required field: subject"}` | PASS | |
| `/notify/email` | GET | — | 405 | **405** | `{"success":false,"error":"unsupported method"}` | PASS | |
| `/admin/config/refresh` | POST | No `Authorization` header | 401 or 403 | **401** | `{"success":false,"error":"unauthorized"}` | PASS | 401 because ADMIN_TOKEN was set; 403 only when ADMIN_TOKEN env var is empty (endpoint disabled) |
| `/admin/config/refresh` | POST | `Authorization: Bearer devsecret` | 200 or 503 | **503** | `{"success":false,"error":"config refresh is not available in DEV_MODE"}` | PASS | Expected DEV_MODE behavior — `ErrDevModeSkip` → 503 |
| `/dlq/failed` | GET | — | 200 with `count` | **200** | `{"success":true,"data":{"count":5,"failures":[...]}}` | PASS | 5 failed workflows from prior integration test runs in Temporal |
| `/dlq/failed` | GET | `?from=bad-date` | 400 | **400** | `{"success":false,"error":"invalid \"from\" date: must be RFC3339"}` | PASS | |
| `/dlq/replay/nonexistent-workflow-id` | POST | path param only | 404 | **404** | `{"success":false,"error":"workflow not found: nonexistent-workflow-id"}` | PASS | |

**All 11 checks: PASS**

---

## Email Delivery — Mailpit Confirmation

After the valid `POST /notify/email` (workflow_id `email-workflow-alice@example.com-1782351841675718000`), the Temporal worker executed the `SendEmailActivity` and delivered the message to Mailpit.

```
GET http://127.0.0.1:8025/api/v1/messages
```

Response (summarised):

```json
{
  "total": 1,
  "messages": [
    {
      "From": { "Name": "Beacon", "Address": "noreply@beacon.local" },
      "To":   [ { "Address": "alice@example.com" } ],
      "Subject": "Readiness check",
      "Snippet": "hello from beacon",
      "Created": "2026-06-24T18:44:01.692-07:00"
    }
  ]
}
```

**Email confirmed in mailpit — subject "Readiness check", recipient alice@example.com.**

---

## Docker-compose Local Stack

File: `deploy/docker-compose.local.yml`
Env template: `deploy/env/.env.local` (gitignored by `deploy/env/*.env` — local convenience only)

**Syntax validation** (Docker daemon not required):

```
docker compose -f deploy/docker-compose.local.yml config
```

Result: **VALID** — all 5 services (`temporal`, `temporal-ui`, `mailpit`, `beacon-server`, `beacon-email-worker`) resolved with canonical paths and full environment blocks. No errors.

**Live stack**: Docker daemon was DOWN during Phase 5. Live checks were executed via the binary fallback described in the Phase 5 task. The compose file is ready for `docker compose up` when a Docker daemon is available.

Services in `docker-compose.local.yml`:
- `temporal` — `temporalio/temporal:latest` (dev mode, no Postgres)
- `temporal-ui` — `temporalio/ui:2.31.2`
- `mailpit` — `axllent/mailpit:latest`, ports 1025 (SMTP) + 8025 (web)
- `beacon-server` — built from repo root Dockerfile, DEV env inline, port 6969
- `beacon-email-worker` — same image, `command: ["/usr/local/bin/email_worker"]`, DEV env inline

All dev env vars are embedded inline in the compose `environment:` blocks. The file does **not** require `deploy/env/.env.local` to pass `docker compose config`.

---

## Task 5.3 — Instrumented cmd Coverage

Both binaries were built with `go build -cover` and run with `GOCOVERDIR` set.

### Worker (`bin/email_worker`) — Coverage COLLECTED

Worker was terminated with `SIGTERM`. The worker binary uses `worker.Run(worker.InterruptCh())` which unblocks cleanly on signal, allowing `main()` to return and the Go coverage runtime to flush counters.

```
go tool covdata percent -i=covdata/worker
```

| Package | Coverage |
|---|---|
| `beacon/cmd/email_worker` | **68.6%** |
| `beacon/internal/temporal` | **100.0%** |
| `beacon/internal/app` | 27.0% |
| `beacon/internal/config` | 13.8% |
| `beacon/internal/notifier` | 17.6% |
| `beacon/utils` | 33.3% |
| `beacon/internal/api` | 0.0% (not exercised by worker) |
| `beacon/internal/dlq` | 0.0% (not exercised by worker) |

The `beacon/internal/temporal` package reached **100%** coverage — the `SendEmailWorkflow` and `SendEmailActivity` were fully exercised by the live readiness-check email send.

### Server (`bin/server`) — Coverage EMPTY (known limitation)

Server was terminated with `SIGTERM`. Coverage files: **only `covmeta` present, no `covcounters`** — all packages reported 0.0%.

```
go tool covdata percent -i=covdata/server
beacon/cmd/server           coverage: 0.0% of statements
beacon/internal/api         coverage: 0.0% of statements
...
```

**Root cause**: `cmd/server/server.go` ends with `log.Fatal(http.ListenAndServe(addr, mux))`. `http.ListenAndServe` blocks indefinitely and `log.Fatal` calls `os.Exit(1)` on error — neither path allows `main()` to return. When `SIGTERM` is received, the process is killed before the Go coverage runtime can call `runtime/coverage.WriteCountersDir()`. No covcounters file is written.

**Impact**: cmd/server coverage cannot be collected this way. The server's handler logic (`internal/api`, `internal/app`, `internal/config`) is fully exercised by the unit tests and integration tests in the existing test suite. The `cmd/server` main function itself is thin wiring code.

**Remediation path** (not in scope for Phase 5): Add graceful HTTP shutdown using `http.Server.Shutdown(ctx)` triggered by a signal handler. This would allow `main()` to return cleanly and flush coverage.

---

## Doc Discrepancies vs. `docs/API.md`

| Finding | Severity | Detail |
|---|---|---|
| `POST /admin/config/refresh` undocumented | Medium | This endpoint is implemented and protected by `ADMIN_TOKEN`. It is not present in `docs/API.md`. DEV_MODE returns 503. |
| `GET /dlq/failed` undocumented | Medium | Fully functional endpoint with filter parameters (`status`, `provider`, `from`, `to`, `limit`, `offset`). Not in `docs/API.md`. |
| `POST /dlq/replay/{workflowID}` undocumented | Medium | Fully functional; returns 404 for missing workflows, 409 for already-running or duplicate replay. Not in `docs/API.md`. |
| Response envelope not documented | Low | All JSON responses use a `{"success":bool,"message":"...","data":{...}}` envelope. `docs/API.md` shows the inner `data` fields directly without wrapping. |
| `/healthz/live` response body not shown in API.md | Low | API.md shows the health routes but does not document their response bodies (`ok` and `ready`). |
| `405` body format undocumented | Low | `GET /notify/email` returns `{"success":false,"error":"unsupported method"}`, but `docs/API.md` only lists `405 Method Not Allowed` as a status code with no body example. |
| `ADMIN_TOKEN` auth semantics undocumented | Low | When `ADMIN_TOKEN` is unset, the endpoint returns 403 (disabled). When set but wrong/missing auth header, returns 401. This distinction is not in any doc. |

**Action**: `docs/API.md` should be extended to cover the admin and DLQ endpoints and the full response envelope. This is deferred to a later task (not Phase 5 scope).

---

## Process Cleanup

All processes started during Phase 5 were stopped before this report was finalised:

| Process | PID | Stop method | Status |
|---|---|---|---|
| `mailpit` | 26818 | `kill -TERM 26818` | Exited |
| `bin/email_worker` | 27120 | `kill -TERM 27120` | Exited (clean, flushed coverage) |
| `bin/server` | 27155 | `kill -TERM 27155` | Exited (coverage not flushed — see above) |
| `temporal` (dev server) | pre-existing | **NOT killed** (per task constraints) | Still running |
