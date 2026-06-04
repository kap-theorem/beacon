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
  "body": "This is the email body."
}
```

| Field | Required | Description |
|---|---|---|
| `to` | Yes | Recipient email address |
| `subject` | Yes | Email subject |
| `body` | No | Email body (plain text) |
| `client_hint` | No | Provider category hint. When set, Beacon routes to the SMTP provider registered under that category name. Omit to use the default provider. |

**Response — 202 Accepted:**

```json
{
  "success": true,
  "message": "email notification triggered",
  "data": {
    "workflow_id": "email-workflow-recipient@example.com-1714567890123456789",
    "workflow_run_id": "abc123-...",
    "provider": "sendgrid-transactional"
  }
}
```

Beacon returns immediately after the workflow is started. Delivery happens asynchronously.

**Error responses:**

| Status | Reason |
|---|---|
| `400 Bad Request` | Missing or invalid request body, missing `to` or `subject` |
| `405 Method Not Allowed` | Non-POST request |
| `503 Service Unavailable` | Temporal server is unreachable |
| `500 Internal Server Error` | Workflow failed to start |

---

## Health Checks

```
GET /healthz/live   → 200 OK  (liveness — process is alive)
GET /healthz/ready  → 200 OK  (readiness — server is ready to serve traffic)
```

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

---

## DLQ Endpoints

These endpoints are always registered. When the Temporal server is unreachable, they return `503 Service Unavailable`.

### List failed workflows

```
GET /dlq/failed
```

Returns workflows in a terminal failure state.

### Replay a failed workflow

```
POST /dlq/replay/{workflow_id}
```

Re-enqueues a failed workflow for retry.

---

## Admin Endpoints

### Refresh config

```
POST /admin/config/refresh
```

Triggers an immediate reload of SMTP provider configuration from Infisical. Requires an `Authorization: Bearer <token>` header where the token matches the `ADMIN_TOKEN` environment variable. Returns `403 Forbidden` if `ADMIN_TOKEN` is unset or the token does not match.
