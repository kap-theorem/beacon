# Beacon API Reference

Beacon is an asynchronous notification service backed by [Temporal](https://temporal.io/).
Every send request is authenticated, policy-checked, and acknowledged immediately (HTTP 202);
delivery is executed by a background worker.

**Default base URL**: `http://localhost:6969`
**Port override**: set `SERVER_PORT` environment variable.
**OpenAPI spec**: [`api/openapi.yaml`](../api/openapi.yaml)

---

## Authentication

All `/v1/*` endpoints require a per-service API key. Present it as either header (Bearer takes
priority if both are set):

```
Authorization: Bearer bk_<keyid>_<secret>
```
```
X-API-Key: bk_<keyid>_<secret>
```

Keys are registered per service in the control plane (see
[`docs/CONFIGURATION.md`](CONFIGURATION.md)); only a SHA-256 hash of each key is ever stored
server-side. Two active keys on one service enable zero-downtime rotation.

| Endpoint group | Auth required |
|---|---|
| `POST /v1/notify/{channel}` | Service API key (Bearer or X-API-Key). `ADMIN_TOKEN` is explicitly **rejected** here (403). |
| `GET /v1/dlq/failed` | Service API key, hard-scoped to the caller's own tenant — **or** `ADMIN_TOKEN` for unscoped/cross-tenant access. |
| `POST /v1/dlq/replay/{workflowID}` | Same as above. |
| `POST /admin/config/refresh` | `ADMIN_TOKEN` Bearer token only (outside `/v1`, independent of service keys). |
| `GET /healthz/*` | None |

`ADMIN_TOKEN` is a separate operator credential from service API keys: it grants unscoped access
on the two DLQ endpoints (an operator can inspect/replay across all tenants) but is rejected with
403 on notify — it is not a sending identity.

---

## Response Envelope

All non-health responses use a common JSON envelope:

```json
{
  "success": true,
  "message": "human-readable summary",
  "data": { ... }
}
```

On error the envelope collapses to:

```json
{
  "success": false,
  "error": "human-readable error message"
}
```

Health endpoints return plain text, not JSON.

---

## Health Checks

```
GET /healthz/live
GET /healthz/ready
```

These return plain text, not JSON. The worker binary exposes **no** health endpoints — only the
HTTP server does.

| Endpoint | 200 body | Notes |
|---|---|---|
| `/healthz/live` | `ok` | Liveness — process is alive; no dependencies checked. Never returns 5xx. |
| `/healthz/ready` | `ready` | Readiness — config is loaded **and** Temporal is reachable (`client.CheckHealth`). |

`/healthz/ready` returns `503 Service Unavailable` with body `not ready: <reason>` when either
check fails (e.g. `not ready: temporal: <dial error>`). The result is cached for 5 s so concurrent
probes don't hammer Temporal; each evaluation runs with its own internal 2 s timeout, independent
of any single caller's request context.

---

## Send a Notification

```
POST /v1/notify/{channel}
Content-Type: application/json
Authorization: Bearer bk_<keyid>_<secret>
```

Only `{channel} = email` is implemented in v2; any other value returns 404.

**Authenticated cURL example:**

```bash
curl -s -X POST http://localhost:6969/v1/notify/email \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer bk_k1_local-test-key" \
  -H "Idempotency-Key: receipt-8812" \
  -d '{
    "to": "user@example.com",
    "subject": "Your order has shipped",
    "body": "Track your order at https://example.com/orders/123"
  }'
```

**Request body:**

```json
{
  "to": "recipient@example.com",
  "cc": ["ops@example.com"],
  "bcc": ["audit@example.com"],
  "subject": "Hello from Beacon",
  "body": "This is the email body.",
  "html": false,
  "provider": "sendgrid"
}
```

| Field | Required | Description |
|---|---|---|
| `to` | Yes | Recipient email address (must be a valid RFC 5322 address) |
| `cc` | No | Carbon-copy addresses |
| `bcc` | No | Blind-carbon-copy addresses. `cc` + `bcc` combined must be ≤ 50 addresses. |
| `subject` | Yes | Email subject |
| `body` | No | Email body |
| `html` | No | When `true`, `body` is sent as `text/html`; otherwise `text/plain`. Defaults to `false`. |
| `provider` | No | Explicit provider name; must be in the calling service's allowlist. Omit to use the service's configured default provider. |

The sender identity (`From` address/name) is **never** accepted from the request — it is always
injected server-side from the calling service's policy configuration (policy-locked sender).

### Idempotency-Key

Optional request header: `Idempotency-Key: <1-128 chars of A-Za-z0-9._->`.

When present, the Temporal workflow ID is derived deterministically as
`{channel}-{service}-{Idempotency-Key}`. A second request from the same service on the same
channel with the same key is detected as a duplicate:

- Returns `202` (not an error) with `data.duplicate: true`.
- `data.workflow_id` is the **original** request's workflow ID (not a new one).
- `data.workflow_run_id` is an empty string (no new run was started).

Omit the header to always start a fresh workflow with a unique, time-based ID.

**Response — 202 Accepted:**

```json
{
  "success": true,
  "message": "notification accepted",
  "data": {
    "workflow_id": "email-billing-api-1748900000000000000",
    "workflow_run_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    "provider": "sendgrid",
    "duplicate": false
  }
}
```

**202 response `data` object:**

| Field | Type | Description |
|---|---|---|
| `workflow_id` | string | Temporal workflow ID |
| `workflow_run_id` | string | Temporal run ID (empty on a duplicate response) |
| `provider` | string | Provider selected for this request |
| `duplicate` | boolean | `true` if this request matched an already-accepted `Idempotency-Key` |

Beacon returns immediately after the workflow is started. Delivery happens asynchronously.

**Retry policy** (hardcoded in the workflow):

| Setting | Value |
|---|---|
| Initial interval | 5 s |
| Backoff coefficient | 2.0 |
| Maximum interval | 2 min |
| Maximum attempts | 3 |

If all attempts fail, the workflow closes as Failed and appears in `GET /v1/dlq/failed`.

### Error responses, in request-handling order

Each row is a short-circuit: once one condition matches, later checks are never reached for that
request.

| Order | Status | Condition | Error field value |
|---|---|---|---|
| 1 | `401 Unauthorized` | Missing API key | `"missing API key"` |
| 1 | `401 Unauthorized` | API key does not resolve to a registered service | `"invalid API key"` |
| 1 | `403 Forbidden` | Service is registered but disabled | `"service disabled"` |
| 2 | `403 Forbidden` | Caller authenticated with `ADMIN_TOKEN` | `"admin token cannot send notifications"` |
| — | `503 Service Unavailable` | Temporal client not connected (degraded startup) | `"temporal service not available"` |
| 3 | `404 Not Found` | Unknown `{channel}` | `"unknown channel: <channel>"` |
| 4 | `403 Forbidden` | Channel not in this service's policy | `"channel \"email\" not enabled for service \"<service>\""` |
| 5 | `413 Payload Too Large` | Body exceeds 256 KB | `"request body exceeds 256 KB"` |
| 5 | `400 Bad Request` | Body could not be read | `"failed to read request body"` |
| 6 | `400 Bad Request` | Malformed JSON | `"invalid request body"` |
| 6 | `400 Bad Request` | Missing/invalid `to`, missing `subject`, bad `cc`/`bcc` address, or too many `cc`+`bcc` recipients | e.g. `"missing required field: to"`, `"invalid email address: to"`, `"too many recipients in cc/bcc (max 50)"`, `"invalid email address in cc: <addr>"` |
| 7 | `403 Forbidden` | Requested `provider` outside this service's allowlist | `"provider \"<name>\" not allowed for this service"` |
| 8 | `503 Service Unavailable` | Resolved provider has no loaded config | `"provider \"<name>\" is not configured"` |
| 9 | `400 Bad Request` | `Idempotency-Key` present but malformed | `"invalid Idempotency-Key: 1-128 chars of A-Za-z0-9._-"` |
| 10 | `429 Too Many Requests` | RPM token bucket or daily UTC quota exceeded (`Retry-After` header set) | `"rate limit exceeded"` |
| 11 | `500 Internal Server Error` | Workflow dispatch failed (non-duplicate) | `"failed to trigger notification"` |
| 11 | `202 Accepted` | Workflow dispatched (or duplicate `Idempotency-Key`) | — |

Rate limiting (row 10) is in-memory per (service, channel): a token bucket refilling at `rpm/60`
tokens/sec, plus a daily counter that resets at UTC midnight. Both are configured per service in
the control plane and **reset on process restart** — there is no persistent counter store.

---

## Admin — Config Refresh

```
POST /admin/config/refresh
Authorization: Bearer <ADMIN_TOKEN>
```

This endpoint lives outside `/v1` and uses its own `ADMIN_TOKEN` bearer check — it is unrelated
to the per-service API keys used on `/v1/*`.

Forces an immediate re-fetch of provider, tenant, and service configuration from Infisical and
reloads the in-memory provider registry and auth registry. Useful for propagating control-plane
updates without waiting for the next background poll (default 300 s, see `CONFIG_POLL_INTERVAL`).
A failed refresh reverts to the previously loaded config (fail-closed) — Beacon never runs with a
partially-applied or empty config after a refresh attempt.

```bash
curl -s -X POST http://localhost:6969/admin/config/refresh \
  -H "Authorization: Bearer <value-of-ADMIN_TOKEN>"
```

**Auth semantics:**

| Condition | Status | Error |
|---|---|---|
| `ADMIN_TOKEN` env var not set | `403 Forbidden` | `"admin endpoint disabled"` |
| `ADMIN_TOKEN` set, header absent or wrong | `401 Unauthorized` | `"unauthorized"` |
| Valid token | 200, 500, or 503 (see below) | — |

**Response — 200 OK (production mode):**

```json
{
  "success": true,
  "message": "config refreshed",
  "data": {
    "revision": 42,
    "providers": ["mailgun", "sendgrid"],
    "services": 12
  }
}
```

**200 response `data` object:**

| Field | Type | Description |
|---|---|---|
| `revision` | integer | Config revision after reload |
| `providers` | string[] | Email provider names active after reload |
| `services` | integer | Number of registered services loaded after reload |

**Response — 503 Service Unavailable (DEV_MODE=true):**

```json
{
  "success": false,
  "error": "config refresh is not available in DEV_MODE"
}
```

Config refresh is intentionally disabled in DEV_MODE because there is no control plane to poll.

**Other error responses:**

| Status | Condition |
|---|---|
| `405 Method Not Allowed` | Non-POST request |
| `500 Internal Server Error` | Infisical fetch/validation or registry reload failed |

---

## DLQ — Query Failed Workflows

```
GET /v1/dlq/failed
Authorization: Bearer bk_<keyid>_<secret>
```

Returns closed Temporal workflow executions that ended in a failed, timed-out, or canceled state.

**Tenant scoping**: non-admin callers are hard-scoped to their own tenant — the `tenant` query
parameter is silently ignored for them. Only a caller authenticated with `ADMIN_TOKEN` gets an
unscoped (cross-tenant) view by default, and may pass `tenant` to narrow it to one tenant.

**Query parameters:**

| Parameter | Type | Description |
|---|---|---|
| `status` | string | Filter by terminal status: `Failed`, `TimedOut`, or `Canceled`. Omit for all three. |
| `provider` | string | Filter by provider name (from the workflow memo, falling back to the task-queue provider segment). |
| `from` | RFC 3339 string | Inclusive start of the workflow close-time window. Defaults to 30 days ago. |
| `to` | RFC 3339 string | Inclusive end of the workflow close-time window. Defaults to now. |
| `limit` | integer | Max results to return (default 20, max 100). |
| `offset` | integer | Pagination offset. |
| `tenant` | string | **Admin only.** Narrows an admin's unscoped query. Ignored for non-admin callers. |

Internally, the query pages through Temporal's `ListClosedWorkflowExecutions` (bounded to 10 RPCs
per request) applying status/tenant/provider filters in-process, so a narrow tenant filter cannot
page indefinitely against a large unfiltered result set.

**Pagination example:**

```bash
curl -s "http://localhost:6969/v1/dlq/failed?limit=20&offset=0" -H "Authorization: Bearer bk_k1_local-test-key"
curl -s "http://localhost:6969/v1/dlq/failed?limit=20&offset=20" -H "Authorization: Bearer bk_k1_local-test-key"
```

**Response — 200 OK:**

```json
{
  "success": true,
  "message": "",
  "data": {
    "count": 1,
    "failures": [
      {
        "workflow_id": "email-billing-api-1748900000000000000",
        "run_id": "abc123-...",
        "recipient": "alice@example.com",
        "subject": "Your order has shipped",
        "provider": "sendgrid",
        "service": "billing-api",
        "tenant": "payments",
        "failure_reason": "SMTP connection refused",
        "retry_count": 3,
        "last_attempt_at": "2026-07-10T14:23:00Z",
        "closed_at": "2026-07-10T14:23:05Z",
        "status": "Failed"
      }
    ]
  }
}
```

**200 response `data` object:**

| Field | Type | Description |
|---|---|---|
| `failures` | array | Array of `FailedNotification` objects |
| `count` | integer | Number of items returned |

**`FailedNotification` object:**

| Field | Type | Description |
|---|---|---|
| `workflow_id` | string | Temporal workflow ID |
| `run_id` | string | Temporal run ID |
| `recipient` | string | Original recipient address |
| `subject` | string | Original email subject |
| `provider` | string | Provider that handled the execution |
| `service` | string | Calling service that submitted the original request (from the workflow memo) |
| `tenant` | string | Owning tenant (from the workflow memo) |
| `failure_reason` | string | Last error message from Temporal history |
| `retry_count` | integer | Number of activity retries attempted |
| `last_attempt_at` | string (RFC 3339) | Timestamp of last activity attempt |
| `closed_at` | string (RFC 3339) | Timestamp workflow execution closed |
| `status` | string | `Failed`, `TimedOut`, or `Canceled` |

**Error responses:**

| Status | Condition | Error field value |
|---|---|---|
| `400 Bad Request` | `from` or `to` value is not a valid RFC 3339 timestamp | `invalid "from" date: must be RFC3339` / `invalid "to" date: must be RFC3339` |
| `401 Unauthorized` | Missing or invalid API key | `"missing API key"` / `"invalid API key"` |
| `500 Internal Server Error` | Temporal history query failed | `"failed to query workflow failures"` |
| `503 Service Unavailable` | Temporal client not connected | `"temporal service not available"` |

---

## DLQ — Replay a Failed Workflow

```
POST /v1/dlq/replay/{workflowID}
Authorization: Bearer bk_<keyid>_<secret>
```

Reads the original notification input from the failed workflow's Temporal history and dispatches
a **new** workflow execution using that input; the original execution is preserved. The new
workflow ID is `replay-{workflowID}`. Only workflows in a terminal state (`Failed`, `TimedOut`,
`Canceled`) can be replayed, and a deterministic replay ID (`WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY`)
prevents duplicate replays of the same workflow.

**Tenant scoping**: a non-admin caller may only replay a workflow that belongs to their own
tenant. A cross-tenant attempt returns `404` (not `403`) — existence is not disclosed across
tenants. A caller authenticated with `ADMIN_TOKEN` may replay any workflow regardless of tenant.

**Path parameter:**

| Parameter | Description |
|---|---|
| `{workflowID}` | The `workflow_id` of the failed workflow to replay |

**Response — 202 Accepted:**

```json
{
  "success": true,
  "message": "workflow replay dispatched",
  "data": {
    "new_workflow_id": "replay-email-billing-api-1748900000000000000",
    "new_run_id": "def456-...",
    "original_workflow_id": "email-billing-api-1748900000000000000",
    "provider": "sendgrid"
  }
}
```

**202 response `data` object (`ReplayResult`):**

| Field | Type | Description |
|---|---|---|
| `new_workflow_id` | string | New Temporal workflow ID (`replay-{workflowID}`) |
| `new_run_id` | string | New Temporal run ID |
| `original_workflow_id` | string | The ID passed in the path |
| `provider` | string | Provider used for the replay |

**Error responses:**

| Status | Condition | Error field value |
|---|---|---|
| `400 Bad Request` | Missing workflow ID in path | `"workflow ID is required"` |
| `401 Unauthorized` | Missing or invalid API key | `"missing API key"` / `"invalid API key"` |
| `404 Not Found` | Workflow ID does not exist, **or** belongs to a different tenant than the caller | `"workflow not found: <id>"` |
| `409 Conflict` | Workflow is still running (not in a terminal state) | `"workflow is still running; replay not allowed"` |
| `409 Conflict` | A replay is already in progress for this workflow | `"replay already in progress for workflow: <id>"` |
| `500 Internal Server Error` | Replay dispatch failed | `"replay failed"` |
| `503 Service Unavailable` | Temporal client not connected | `"temporal service not available"` |

---

## Consuming Beacon from an Upstream Service

**Example — cURL:**

```bash
curl -X POST http://beacon-host:6969/v1/notify/email \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer bk_k1_your-service-key" \
  -d '{
    "to": "user@example.com",
    "subject": "Your order has shipped",
    "body": "Track your order at https://example.com/orders/123"
  }'
```

**Example — Go:**

```go
payload := map[string]string{
    "to":      "user@example.com",
    "subject": "Your order has shipped",
    "body":    "Track your order at https://example.com/orders/123",
}
body, _ := json.Marshal(payload)

req, _ := http.NewRequest(http.MethodPost, "http://beacon-host:6969/v1/notify/email", bytes.NewReader(body))
req.Header.Set("Content-Type", "application/json")
req.Header.Set("Authorization", "Bearer bk_k1_your-service-key")

resp, err := http.DefaultClient.Do(req)
if err != nil {
    // handle connection error
}
defer resp.Body.Close()

if resp.StatusCode == http.StatusAccepted {
    var result struct {
        Success bool `json:"success"`
        Data    struct {
            WorkflowID    string `json:"workflow_id"`
            WorkflowRunID string `json:"workflow_run_id"`
            Provider      string `json:"provider"`
            Duplicate     bool   `json:"duplicate"`
        } `json:"data"`
    }
    json.NewDecoder(resp.Body).Decode(&result)
    // email workflow started — delivery is async
}
```

**Example — Python:**

```python
import requests

response = requests.post(
    "http://beacon-host:6969/v1/notify/email",
    headers={"Authorization": "Bearer bk_k1_your-service-key"},
    json={
        "to": "user@example.com",
        "subject": "Your order has shipped",
        "body": "Track your order at https://example.com/orders/123",
    }
)

if response.status_code == 202:
    data = response.json()["data"]
    print("workflow started:", data["workflow_id"])
    print("provider:", data["provider"])
```

The `202 Accepted` response means the workflow was enqueued. Email delivery is asynchronous — the
upstream service does not need to wait or poll, though it may query `GET /v1/dlq/failed` later to
check for terminal failures.
