# Beacon — API Reference

All responses use a common JSON envelope:

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

The sections below show only the `data` fields in success examples; the outer envelope is always present.

---

## Health Checks

```
GET /healthz/live
GET /healthz/ready
```

These endpoints return plain text — not JSON.

| Endpoint | 200 body | Notes |
|---|---|---|
| `/healthz/live` | `ok` | Liveness — process is alive |
| `/healthz/ready` | `ready` | Readiness — server is ready to serve traffic |

`/healthz/ready` returns `503 Service Unavailable` when the server is not yet ready.

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
| `503 Service Unavailable` | Temporal client not connected at startup | `"temporal service not available"` |

---

## Admin — Config Refresh

```
POST /admin/config/refresh
Authorization: Bearer <ADMIN_TOKEN>
```

Forces an immediate re-fetch of SMTP provider configuration from Infisical and reloads the email client registry. Requires `ADMIN_TOKEN` to be set in the server's environment.

**Auth semantics:**

| Condition | Status | Error |
|---|---|---|
| `ADMIN_TOKEN` env var not set | `403 Forbidden` | `"admin endpoint disabled"` |
| `ADMIN_TOKEN` set, header absent or wrong | `401 Unauthorized` | `"unauthorized"` |
| Valid token | 200 or 503 (see below) | — |

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

**Response — 503 Service Unavailable (DEV_MODE=true):**

```json
{
  "success": false,
  "error": "config refresh is not available in DEV_MODE"
}
```

Config refresh is intentionally disabled in DEV_MODE because there is no Infisical backend to poll.

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
| `from` | RFC 3339 string | Inclusive start of the workflow close-time window. |
| `to` | RFC 3339 string | Inclusive end of the workflow close-time window. |
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

**Error responses:**

| Status | Condition |
|---|---|
| `400 Bad Request` | `from` or `to` value is not a valid RFC 3339 timestamp |
| `503 Service Unavailable` | Temporal client not connected at startup |

---

## DLQ — Replay a Failed Workflow

```
POST /dlq/replay/{workflowID}
```

Dispatches a new Temporal workflow execution using the original input from the failed workflow. The new workflow ID is `replay-{workflowID}`. Temporal's `ALLOW_DUPLICATE_FAILED_ONLY` reuse policy prevents duplicate replays.

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

**Error responses:**

| Status | Condition | Error field value |
|---|---|---|
| `400 Bad Request` | Missing workflow ID in path | `"workflow ID is required"` |
| `404 Not Found` | Workflow ID does not exist in Temporal | `"workflow not found: <id>"` |
| `409 Conflict` | Workflow is still running (not in a terminal state) | `"workflow is still running; replay not allowed"` |
| `409 Conflict` | A replay is already in progress for this workflow | `"replay already in progress for workflow: <id>"` |
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
