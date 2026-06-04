# Beacon — API Reference

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
| `to` | Yes | Recipient email address |
| `subject` | Yes | Email subject |
| `body` | No | Email body (plain text) |
| `client_hint` | No | Routing category hint; selects the matching SMTP provider |

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

| Status | Reason |
|---|---|
| `400 Bad Request` | Missing or invalid request body, missing `to` or `subject`, or no matching provider for `client_hint` |
| `405 Method Not Allowed` | Non-POST request |
| `503 Service Unavailable` | Temporal server is unreachable |
| `500 Internal Server Error` | Workflow failed to start |

---

## Health Checks

```
GET /healthz/live   → 200 OK, body: "ok"    (liveness — process is alive)
GET /healthz/ready  → 200 OK, body: "ready" (readiness — server is ready to serve traffic)
```

---

## Dead Letter Queue

### Query Failed Workflows

```
GET /dlq/failed
```

Returns failed email workflows from Temporal history. If Temporal was unavailable when the server started, returns `503`. If Temporal becomes unavailable at runtime, returns `500`.

**Query parameters:**

| Parameter | Description |
|---|---|
| `status` | Filter by workflow status (e.g. `Failed`, `TimedOut`, `Cancelled`) |
| `provider` | Filter by provider name (e.g. `sendgrid`, `mailgun`) |
| `from` | Start of date range (RFC3339, e.g. `2026-01-01T00:00:00Z`) |
| `to` | End of date range (RFC3339) |
| `limit` | Max results to return (capped at 100) |
| `offset` | Pagination offset |

**Response — 200 OK:**

```json
{
  "success": true,
  "data": {
    "failures": [...],
    "count": 3
  }
}
```

### Replay a Failed Workflow

```
POST /dlq/replay/{workflowID}
```

Re-dispatches a failed workflow as a new execution. The original workflow record is preserved in Temporal history.

**Response — 202 Accepted:**

```json
{
  "success": true,
  "message": "workflow replay dispatched",
  "data": {
    "new_workflow_id": "email-workflow-...",
    "new_run_id": "def456-...",
    "original_workflow_id": "email-workflow-...",
    "provider": "sendgrid"
  }
}
```

**Error responses:**

| Status | Reason |
|---|---|
| `404 Not Found` | Workflow ID not found |
| `409 Conflict` | Workflow is still running; replay not allowed |
| `500 Internal Server Error` | Replay failed (Temporal error or unexpected failure) |
| `503 Service Unavailable` | Temporal was unavailable at server startup |

---

## Admin

### Refresh Config

```
POST /admin/config/refresh
Authorization: Bearer <ADMIN_TOKEN>
```

Triggers an immediate config reload from Infisical and reloads the email client registry. Requires the `ADMIN_TOKEN` environment variable to be set; returns `403` when it is unset (endpoint effectively disabled).

> **Note:** When `DEV_MODE=true`, this endpoint will attempt to reach Infisical and fail. Config refresh has no effect in dev mode.

**Response — 200 OK:**

```json
{
  "success": true,
  "message": "config refreshed",
  "data": {
    "revision": 5,
    "providers": ["sendgrid", "mailgun"]
  }
}
```

**Error responses:**

| Status | Reason |
|---|---|
| `401 Unauthorized` | Bearer token does not match `ADMIN_TOKEN` |
| `403 Forbidden` | `ADMIN_TOKEN` env var is not set |
| `405 Method Not Allowed` | Non-POST request |
| `500 Internal Server Error` | Config refresh or registry reload failed |

---

## Consuming Beacon from an Upstream Service

Any service that can make HTTP requests can send emails through Beacon.

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
    data = response.json()
    print("workflow started:", data["data"]["workflow_id"])
```

The `202 Accepted` response means the workflow was enqueued. Email delivery is asynchronous — the upstream service does not need to wait or poll.
