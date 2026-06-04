# Beacon API — Usage Examples

All examples assume Beacon is running on `localhost:6969` (default port).

---

## 1. Send an email notification

### Minimal request

```bash
curl -s -X POST http://localhost:6969/notify/email \
  -H "Content-Type: application/json" \
  -d '{
    "to": "user@example.com",
    "subject": "Hello from Beacon",
    "body": "This is a test message."
  }'
```

**Expected response (HTTP 202)**:

```json
{
  "success": true,
  "message": "email notification triggered",
  "data": {
    "workflow_id": "email-workflow-user@example.com-1748900000000000000",
    "workflow_run_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    "provider": "sendgrid"
  }
}
```

Save the `workflow_id` to check delivery status in Temporal UI or via the DLQ endpoints.

---

### Request with explicit provider routing (`client_hint`)

Use `client_hint` to route to a specific provider category (e.g., `transactional`, `marketing`)
or an exact provider name (e.g., `sendgrid`). When omitted, the default provider is used.

```bash
curl -s -X POST http://localhost:6969/notify/email \
  -H "Content-Type: application/json" \
  -d '{
    "to": "ops@example.com",
    "subject": "Disk usage alert",
    "body": "Disk usage on prod-1 is at 95%.",
    "client_hint": "transactional"
  }'
```

---

### Error — missing required fields (HTTP 400)

```bash
curl -s -X POST http://localhost:6969/notify/email \
  -H "Content-Type: application/json" \
  -d '{"to": "user@example.com"}'
```

```json
{
  "success": false,
  "error": "missing required fields: to, subject"
}
```

---

## 2. Query failed notifications (DLQ)

### List all failures (last 20)

```bash
curl -s http://localhost:6969/dlq/failed
```

**Expected response (HTTP 200)**:

```json
{
  "success": true,
  "data": {
    "failures": [
      {
        "workflow_id": "email-workflow-user@example.com-1748900000000000000",
        "run_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
        "recipient": "user@example.com",
        "subject": "Hello from Beacon",
        "provider": "sendgrid",
        "failure_reason": "SMTP connection refused",
        "retry_count": 3,
        "last_attempt_at": "2025-06-01T12:34:56Z",
        "closed_at": "2025-06-01T12:35:10Z",
        "status": "Failed"
      }
    ],
    "count": 1
  }
}
```

---

### Filter by status and provider

```bash
curl -s "http://localhost:6969/dlq/failed?status=Failed&provider=sendgrid&limit=10"
```

---

### Filter by date range

```bash
curl -s "http://localhost:6969/dlq/failed?from=2025-06-01T00:00:00Z&to=2025-06-30T23:59:59Z"
```

---

### Paginate through large result sets

```bash
# First page
curl -s "http://localhost:6969/dlq/failed?limit=20&offset=0"

# Second page
curl -s "http://localhost:6969/dlq/failed?limit=20&offset=20"
```

---

## 3. Replay a failed notification

Use the `workflow_id` from the DLQ query to dispatch a fresh execution.
The original failed workflow is preserved in Temporal history.

```bash
curl -s -X POST \
  http://localhost:6969/dlq/replay/email-workflow-user@example.com-1748900000000000000
```

**Expected response (HTTP 202)**:

```json
{
  "success": true,
  "message": "workflow replay dispatched",
  "data": {
    "new_workflow_id": "replay-email-workflow-user@example.com-1748900000000000000-1748900001000000000",
    "new_run_id": "b2c3d4e5-f6a7-8901-bcde-f12345678901",
    "original_workflow_id": "email-workflow-user@example.com-1748900000000000000",
    "provider": "sendgrid"
  }
}
```

Track delivery of the replay using `new_workflow_id`.

---

### Error — workflow still running (HTTP 409)

```bash
curl -s -X POST http://localhost:6969/dlq/replay/email-workflow-user@example.com-1748900000000000000
```

```json
{
  "success": false,
  "error": "workflow is still running; replay not allowed"
}
```

---

### Error — workflow not found (HTTP 404)

```json
{
  "success": false,
  "error": "workflow not found: email-workflow-user@example.com-1748900000000000000"
}
```

---

## 4. Force config reload (admin)

Trigger an immediate re-fetch of SMTP configurations from Infisical.
Requires the `ADMIN_TOKEN` environment variable to be set on the server.

```bash
curl -s -X POST http://localhost:6969/admin/config/refresh \
  -H "Authorization: Bearer my-secret-admin-token"
```

**Expected response (HTTP 200)**:

```json
{
  "success": true,
  "message": "config refreshed",
  "data": {
    "revision": 42,
    "providers": ["sendgrid", "mailgun"]
  }
}
```

---

### Error — admin endpoint disabled (HTTP 403)

Returned when `ADMIN_TOKEN` is not set on the server:

```json
{
  "success": false,
  "error": "admin endpoint disabled"
}
```

---

### Error — wrong token (HTTP 401)

```json
{
  "success": false,
  "error": "unauthorized"
}
```

---

## 5. Health checks

```bash
# Liveness — is the process alive?
curl -s http://localhost:6969/healthz/live
# → ok  (HTTP 200)

# Readiness — is the server ready to handle traffic?
curl -s http://localhost:6969/healthz/ready
# → ready      (HTTP 200 — startup complete)
# → not ready  (HTTP 503 — still initializing)
```

---

## Go client example

```go
package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
)

type EmailRequest struct {
    To         string `json:"to"`
    Subject    string `json:"subject"`
    Body       string `json:"body"`
    ClientHint string `json:"client_hint,omitempty"`
}

type APIResponse struct {
    Success bool            `json:"success"`
    Message string          `json:"message"`
    Data    json.RawMessage `json:"data"`
    Error   string          `json:"error"`
}

func sendEmail(baseURL string, req EmailRequest) (string, error) {
    payload, _ := json.Marshal(req)
    resp, err := http.Post(baseURL+"/notify/email", "application/json", bytes.NewReader(payload))
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()

    var apiResp APIResponse
    json.NewDecoder(resp.Body).Decode(&apiResp)

    if !apiResp.Success {
        return "", fmt.Errorf("beacon error: %s", apiResp.Error)
    }

    var data map[string]string
    json.Unmarshal(apiResp.Data, &data)
    return data["workflow_id"], nil
}

func main() {
    wfID, err := sendEmail("http://localhost:6969", EmailRequest{
        To:      "user@example.com",
        Subject: "Hello",
        Body:    "World",
    })
    if err != nil {
        panic(err)
    }
    fmt.Println("dispatched:", wfID)
}
```

---

## Python client example

```python
import requests

BASE_URL = "http://localhost:6969"

def send_email(to: str, subject: str, body: str = "", client_hint: str = "") -> str:
    payload = {"to": to, "subject": subject, "body": body}
    if client_hint:
        payload["client_hint"] = client_hint

    resp = requests.post(f"{BASE_URL}/notify/email", json=payload)
    resp.raise_for_status()
    data = resp.json()
    if not data["success"]:
        raise RuntimeError(data["error"])
    return data["data"]["workflow_id"]


def list_failures(status: str = "", provider: str = "", limit: int = 20) -> list:
    params = {"limit": limit}
    if status:
        params["status"] = status
    if provider:
        params["provider"] = provider
    resp = requests.get(f"{BASE_URL}/dlq/failed", params=params)
    resp.raise_for_status()
    return resp.json()["data"]["failures"]


def replay(workflow_id: str) -> dict:
    resp = requests.post(f"{BASE_URL}/dlq/replay/{workflow_id}")
    resp.raise_for_status()
    return resp.json()["data"]


if __name__ == "__main__":
    wf_id = send_email("user@example.com", "Hello from Python")
    print("dispatched:", wf_id)

    failures = list_failures(status="Failed")
    for f in failures:
        print(f["workflow_id"], f["failure_reason"])
```
