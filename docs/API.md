# Beacon API Reference

Beacon is an asynchronous email notification service backed by [Temporal](https://temporal.io/).
Every send request is acknowledged immediately (HTTP 202) and executed by a background worker.

**Default base URL**: `http://localhost:6969`  
**Port override**: set `SERVER_PORT` environment variable.  
**OpenAPI spec**: [`api/openapi.yaml`](../api/openapi.yaml)

---

## Authentication

| Endpoint group | Auth required |
|---|---|
| `POST /notify/email` | None (MVP — internal use) |
| `GET /dlq/failed` | None |
| `POST /dlq/replay/{workflowID}` | None |
| `POST /admin/config/refresh` | Bearer token (`ADMIN_TOKEN`) |
| `GET /healthz/*` | None |

For the admin endpoint, pass the token in the `Authorization` header:

```
Authorization: Bearer <value-of-ADMIN_TOKEN>
```

If `ADMIN_TOKEN` is not set on the server the endpoint returns HTTP 403 (disabled).

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

These endpoints return plain text — not JSON.

| Endpoint | 200 body | Notes |
|---|---|---|
| `/healthz/live` | `ok` | Liveness — process is alive; no external dependencies checked |
| `/healthz/ready` | `ready` | Readiness — startup is complete (config loaded, registry built) |

`/healthz/ready` returns `503 Service Unavailable` with body `not ready` during startup or after a fatal initialization failure.

---

## Send an Email

```
POST /notify/email
Content-Type: application/json
```

**Request body:**

```json
{
  "to": "recipient@example.com",
  "subject": "Hello from Beacon",
  "body": "This is the email body.",
  "client_hint": "payments"
}
```

| Field | Required | Description |
|---|---|---|
| `to` | Yes | Recipient email address (must be a valid RFC 5322 address) |
| `subject` | Yes | Email subject |
| `body` | No | Email body (plain text) |
| `client_hint` | No | Routing category hint; selects the SMTP provider when multiple are configured. Omit to use the default provider. |

**Response — 202 Accepted:**

```json
{
  "success": true,
  "message": "email notification triggered",
  "data": {
    "workflow_id": "email-workflow-recipient@example.com-1714567890123456789",
    "workflow_run_id": "abc123-...",
    "provider": "sendgrid"
  }
}
```

**202 response `data` object:**

| Field | Type | Description |
|---|---|---|
| `workflow_id` | string | Temporal workflow ID |
| `workflow_run_id` | string | Temporal run ID |
| `provider` | string | Email provider selected for this request |

Beacon returns immediately after the workflow is started. Delivery happens asynchronously.

**Error responses:**

| Status | Condition | Error field value |
|---|---|---|
| `400 Bad Request` | Malformed JSON body | `"invalid request body"` |
| `400 Bad Request` | Missing `to` field | `"missing required field: to"` |
| `400 Bad Request` | Missing `subject` field | `"missing required field: subject"` |
| `400 Bad Request` | Invalid email in `to` | `"invalid email address: to"` |
| `400 Bad Request` | Unknown `client_hint` routing category | `"routing error: ..."` |
| `405 Method Not Allowed` | Non-POST request | `"unsupported method"` |
| `500 Internal Server Error` | Temporal workflow dispatch failed | `"failed to trigger email notification"` |
| `503 Service Unavailable` | Temporal client not connected at startup | `"temporal service not available"` |

---

## Admin — Config Refresh

```
POST /admin/config/refresh
Authorization: Bearer <ADMIN_TOKEN>
```

Forces an immediate re-fetch of SMTP provider configuration from Infisical and reloads the in-memory email client registry. Useful for propagating secret updates without waiting for the next background poll (default 300 s). Requires `ADMIN_TOKEN` to be set in the server's environment.

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
    "providers": ["mailgun", "sendgrid"]
  }
}
```

**200 response `data` object:**

| Field | Type | Description |
|---|---|---|
| `revision` | integer | Config revision after reload |
| `providers` | string[] | Provider names active after reload |

**Response — 503 Service Unavailable (DEV_MODE=true):**

```json
{
  "success": false,
  "error": "config refresh is not available in DEV_MODE"
}
```

Config refresh is intentionally disabled in DEV_MODE because there is no Infisical backend to poll.

**Other error responses:**

| Status | Condition |
|---|---|
| `405 Method Not Allowed` | Non-POST request |
| `500 Internal Server Error` | Infisical fetch or registry reload failed |

---

## DLQ — Query Failed Workflows

```
GET /dlq/failed
```

Returns closed Temporal workflow executions that ended in a failed, timed-out, or canceled state.

**Query parameters:**

| Parameter | Type | Description |
|---|---|---|
| `status` | string | Filter by terminal status: `Failed`, `TimedOut`, or `Canceled`. Omit for all three. |
| `provider` | string | Filter by SMTP provider name (e.g. `sendgrid`). |
| `from` | RFC 3339 string | Inclusive start of the workflow close-time window. Defaults to 30 days ago. |
| `to` | RFC 3339 string | Inclusive end of the workflow close-time window. Defaults to now. |
| `limit` | integer | Max results to return (default 20, max 100). |
| `offset` | integer | Pagination offset. |

**Response — 200 OK:**

```json
{
  "success": true,
  "message": "",
  "data": {
    "count": 2,
    "failures": [
      {
        "workflow_id": "email-workflow-alice@example.com-1714567890123",
        "run_id": "abc123-...",
        "recipient": "alice@example.com",
        "subject": "Your order has shipped",
        "provider": "sendgrid",
        "failure_reason": "SMTP connection refused",
        "retry_count": 3,
        "last_attempt_at": "2026-06-10T14:23:00Z",
        "closed_at": "2026-06-10T14:23:05Z",
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
| `failure_reason` | string | Last error message from Temporal history |
| `retry_count` | integer | Number of activity retries attempted |
| `last_attempt_at` | string (RFC 3339) | Timestamp of last activity attempt |
| `closed_at` | string (RFC 3339) | Timestamp workflow execution closed |
| `status` | string | `Failed`, `TimedOut`, or `Canceled` |

**Error responses:**

| Status | Condition |
|---|---|
| `400 Bad Request` | `from` or `to` value is not a valid RFC 3339 timestamp |
| `405 Method Not Allowed` | Non-GET request |
| `500 Internal Server Error` | Temporal history query failed |
| `503 Service Unavailable` | Temporal client not connected at startup |

---

## DLQ — Replay a Failed Workflow

```
POST /dlq/replay/{workflowID}
```

Reads the original `EmailMessage` from the failed workflow's Temporal history and dispatches a **new** workflow execution using that input; the original execution is preserved. The new workflow ID is `replay-{workflowID}`. Only workflows in a terminal state (`Failed`, `TimedOut`, `Canceled`) can be replayed, and Temporal's `ALLOW_DUPLICATE_FAILED_ONLY` reuse policy prevents duplicate replays.

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
    "new_workflow_id": "replay-email-workflow-alice@example.com-1714567890123",
    "new_run_id": "def456-...",
    "original_workflow_id": "email-workflow-alice@example.com-1714567890123",
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
| `404 Not Found` | Workflow ID does not exist in Temporal | `"workflow not found: <id>"` |
| `405 Method Not Allowed` | Non-POST request | `"method not allowed"` |
| `409 Conflict` | Workflow is still running (not in a terminal state) | `"workflow is still running; replay not allowed"` |
| `409 Conflict` | A replay is already in progress for this workflow | `"replay already in progress for workflow: <id>"` |
| `500 Internal Server Error` | Replay dispatch failed | `"replay failed"` |
| `503 Service Unavailable` | Temporal client not connected at startup | `"temporal service not available"` |

---

## Consuming Beacon from an Upstream Service

**Example — cURL:**

```bash
curl -X POST http://beacon-host:6969/notify/email \
  -H "Content-Type: application/json" \
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

resp, err := http.Post("http://beacon-host:6969/notify/email", "application/json", bytes.NewReader(body))
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
    "http://beacon-host:6969/notify/email",
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

The `202 Accepted` response means the workflow was enqueued. Email delivery is asynchronous — the upstream service does not need to wait or poll.
