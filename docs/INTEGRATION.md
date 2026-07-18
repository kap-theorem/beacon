# Beacon — Downstream Integration Guide

**Audience**: Engineers integrating an upstream service (auth service, order service, etc.) with Beacon to send email notifications.
**Beacon version**: `fix/known-issues` branch (feature-complete, Phase 7)
**Last verified**: 2026-06-24 against live stack (see `docs/FEATURE_READINESS.md`)

---

## Table of Contents

1. [What Beacon Is](#1-what-beacon-is)
2. [Beacon-Side Setup (Operator)](#2-beacon-side-setup-operator)
3. [Endpoint, Method, Headers, and Request Schema](#3-endpoint-method-headers-and-request-schema)
4. [Response Shape and Status Codes](#4-response-shape-and-status-codes)
5. [Async Semantics, Retries, and the DLQ](#5-async-semantics-retries-and-the-dlq)
6. [Integration Examples](#6-integration-examples)
7. [Authentication Note](#7-authentication-note)

---

## 1. What Beacon Is

Beacon is a self-hosted notification dispatch service. Its primary role is to accept an email send request, immediately enqueue a [Temporal](https://temporal.io/) workflow for asynchronous delivery, and return a `202 Accepted` response to the caller. The calling service does not wait for delivery; it records the `workflow_id` returned in the response and moves on.

### The async contract

```
Caller                 Beacon                       Temporal Worker
  |                      |                                |
  |-- POST /notify/email -->                              |
  |                      |-- ExecuteWorkflow ------------>|
  |<-- 202 Accepted ------|                               |
  |   (workflow_id,       |                    (worker dials SMTP,
  |    workflow_run_id,   |                     delivers email,
  |    provider)          |                     retries on failure)
```

**Key properties:**

- Beacon returns `202` as soon as the workflow is enqueued. At that point, email delivery has not yet occurred.
- Actual SMTP delivery happens inside the Temporal worker's `SendEmailActivity`, which runs asynchronously after Beacon has already responded.
- If the SMTP server is temporarily unavailable, the Temporal retry policy re-attempts delivery automatically (see [Section 5](#5-async-semantics-retries-and-the-dlq)).
- A `202` response does not guarantee delivery. It guarantees that the delivery workflow has been durably enqueued in Temporal. Workflows that exhaust all retries land in the Dead Letter Queue (DLQ), queryable at `GET /dlq/failed`.

---

## 2. Beacon-Side Setup (Operator)

Before a downstream service can send its first notification, the Beacon operator must complete this configuration.

### 2.1 Add the SMTP provider in Infisical

Beacon reads SMTP provider configuration from Infisical at the path `/beacon/smtp`. Each provider entry is a JSON-encoded `SMTPClientConfig`. The key fields relevant to routing are:

```json
{
  "name": "mailgun-transactional",
  "host": "smtp.mailgun.org",
  "port": 587,
  "username": "postmaster@mg.example.com",
  "password": "...",
  "from_address": "noreply@example.com",
  "from_name": "Example App",
  "categories": ["transactional", "otp"],
  "is_default": true
}
```

Key fields:

| Field | Purpose |
|---|---|
| `name` | The internal provider key. Returned in the `202` response as `provider`. |
| `categories` | List of routing category strings this provider handles. A downstream service sends one of these strings as `client_hint`. |
| `is_default` | When `true`, requests with no `client_hint` (or an unrecognized one) are routed here. |

**At minimum one provider must exist and either have `is_default: true` or be the only configured provider**, so that calls without a `client_hint` succeed.

### 2.2 Agree on the `client_hint` value

Coordinate with the Beacon operator to establish which routing category your service will use. Example agreed values:

| Downstream service | `client_hint` value |
|---|---|
| Auth service (OTPs) | `"otp"` |
| Order service (receipts) | `"transactional"` |
| Marketing system | `"marketing"` |

The operator must ensure the agreed-upon string appears in the `categories` array of the intended provider in Infisical.

### 2.3 Default routing when `client_hint` is omitted or unknown

Routing resolution in `internal/notifier/registry.go` follows this precedence:

1. If `client_hint` is non-empty and matches a configured category, that provider is used.
2. If `client_hint` is empty **or** does not match any category, the default provider is used (the one with `is_default: true`, or the sole provider if only one is configured).
3. If no default exists and the hint has no match, the request returns `400` with `routing error: no email client for hint "..." and no default provider configured`.

Downstream services that do not need category routing may omit `client_hint` entirely, as long as a default provider is configured.

### 2.4 Temporal task queue naming

Beacon derives the Temporal task queue name from the resolved provider name using the pattern `email-<provider-name>-queue`. The Temporal worker must be subscribed to the same queue. For the example above with `name: "mailgun-transactional"`, the task queue is `email-mailgun-transactional-queue`. This is an internal concern but matters when standing up workers.

---

## 3. Endpoint, Method, Headers, and Request Schema

### Endpoint

```
POST /notify/email
```

### Headers

| Header | Value | Required |
|---|---|---|
| `Content-Type` | `application/json` | Yes |

No application-layer authentication header is required today (see [Section 7](#7-authentication-note)).

### Request body

```json
{
  "to":          "recipient@example.com",
  "subject":     "Your verification code",
  "body":        "Your code is 482910. It expires in 10 minutes.",
  "client_hint": "otp"
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `to` | string | Yes | Recipient email address. Leading/trailing whitespace is trimmed before validation. Must be a valid RFC 5322 address. |
| `subject` | string | Yes | Email subject line. Must be non-empty (not trimmed; a whitespace-only subject is accepted at the API layer but discouraged). |
| `body` | string | No | Plain-text email body. If omitted, the email is sent with an empty body. |
| `client_hint` | string | No | Routing category key. When present and recognized, routes to the matching SMTP provider. When absent or unrecognized, falls back to the default provider. |

### Validation rules and order

The handler in `internal/api/email.go` validates in this exact order:

1. Method must be `POST` — otherwise `405`.
2. Temporal client must be available — otherwise `503`.
3. Request body must be valid JSON — otherwise `400 invalid request body`.
4. `to` (after trimming) must not be empty — otherwise `400 missing required field: to`.
5. `subject` must not be empty — otherwise `400 missing required field: subject`.
6. Trimmed `to` must parse as an RFC 5322 address — otherwise `400 invalid email address: to`.
7. `client_hint` routing must resolve — otherwise `400 routing error: ...`.

---

## 4. Response Shape and Status Codes

### Envelope structure

All Beacon responses use a consistent JSON envelope defined in `utils/http_response.go`:

```
{
  "success": bool,
  "message": string,   // present on success
  "data":    object,   // present on success; shape varies by endpoint
  "error":   string    // present on failure; "message" and "data" are absent
}
```

Success and error keys are mutually exclusive: a success response has `message` + `data`; an error response has `error` only.

### Status codes and real response bodies

The examples below are taken directly from verified live captures in `docs/FEATURE_READINESS.md`.

#### 202 Accepted — workflow enqueued

```json
{
  "success": true,
  "message": "email notification triggered",
  "data": {
    "provider":        "dev",
    "workflow_id":     "email-workflow-alice@example.com-1782351841675718000",
    "workflow_run_id": "019efc72-c98d-7a0a-b07c-24bcf2f245f3"
  }
}
```

The `data` fields:

| Field | Description |
|---|---|
| `workflow_id` | Stable Temporal workflow identifier. Format: `email-workflow-<to>-<unix-nanoseconds>`. Use this to look up the workflow in Temporal UI or query the DLQ. |
| `workflow_run_id` | Temporal run ID for this specific execution attempt. Useful for precise replay targeting. |
| `provider` | The SMTP provider name that was selected for this workflow. Reflects the routing outcome. |

**Save `workflow_id`** in your service's own datastore if you need the ability to query or replay the workflow later.

#### 400 Bad Request — invalid email address

```json
{
  "success": false,
  "error": "invalid email address: to"
}
```

#### 400 Bad Request — missing required field

```json
{
  "success": false,
  "error": "missing required field: subject"
}
```

#### 405 Method Not Allowed — non-POST request

```json
{
  "success": false,
  "error": "unsupported method"
}
```

#### 503 Service Unavailable — Temporal unreachable

```json
{
  "success": false,
  "error": "temporal service not available"
}
```

Beacon returns `503` when its Temporal client is `nil` (Temporal server was unreachable at startup). This is distinct from a transient delivery failure: the workflow was never enqueued.

#### 500 Internal Server Error — workflow dispatch failed

```json
{
  "success": false,
  "error": "failed to trigger email notification"
}
```

Temporal accepted the connection but `ExecuteWorkflow` returned an error. Treat this as a retriable error from the caller's perspective.

---

## 5. Async Semantics, Retries, and the DLQ

### Retry policy

Once a workflow is enqueued, the Temporal worker's `SendEmailActivity` is governed by the retry policy defined in `internal/temporal/workflow_email.go`:

| Parameter | Value |
|---|---|
| `InitialInterval` | 5 seconds |
| `BackoffCoefficient` | 2.0 |
| `MaximumInterval` | 2 minutes |
| `MaximumAttempts` | 3 |

Retry timing: attempt 1 fails → wait 5 s → attempt 2 fails → wait 10 s (capped at 2 min) → attempt 3 fails → workflow is closed as `Failed`.

The `StartToCloseTimeout` for the activity is also 2 minutes, meaning each individual SMTP dial attempt has a 2-minute deadline before it is considered failed and a retry is scheduled.

After 3 failed attempts the workflow transitions to a terminal `Failed` state and becomes visible in the DLQ.

### Querying failed workflows — `GET /dlq/failed`

```
GET /dlq/failed
GET /dlq/failed?status=Failed&provider=dev&limit=20
```

Query parameters (all optional):

| Parameter | Type | Description |
|---|---|---|
| `status` | string | Filter by terminal state: `Failed`, `TimedOut`, or `Canceled`. Omit for all three. |
| `provider` | string | Filter by provider name (matches the task-queue provider name). |
| `from` | RFC3339 string | Inclusive lower bound on workflow close time. |
| `to` | RFC3339 string | Inclusive upper bound on workflow close time. |
| `limit` | integer | Max results per page. Default 20, max 100. |
| `offset` | integer | Pagination offset. |

**Example response (from live verification, 5 failures present):**

```json
{
  "success": true,
  "data": {
    "count": 5,
    "failures": [
      {
        "workflow_id":     "email-workflow-alice@example.com-...",
        "run_id":          "...",
        "recipient":       "alice@example.com",
        "subject":         "...",
        "provider":        "dev",
        "failure_reason":  "...",
        "retry_count":     3,
        "last_attempt_at": "2026-06-24T...",
        "closed_at":       "2026-06-24T...",
        "status":          "Failed"
      }
    ]
  }
}
```

**Invalid `from` date — 400 Bad Request:**

```json
{
  "success": false,
  "error": "invalid \"from\" date: must be RFC3339"
}
```

### Replaying a failed workflow — `POST /dlq/replay/{workflowID}`

Replaying dispatches a new Temporal workflow execution for the same email message. The original workflow must be in a terminal state (`Failed`, `TimedOut`, or `Canceled`).

```
POST /dlq/replay/email-workflow-alice@example.com-1782351841675718000
```

**202 Accepted — replay dispatched:**

```json
{
  "success": true,
  "message": "workflow replay dispatched",
  "data": {
    "new_workflow_id":      "email-workflow-alice@example.com-...",
    "new_run_id":           "...",
    "original_workflow_id": "email-workflow-alice@example.com-1782351841675718000",
    "provider":             "dev"
  }
}
```

**404 Not Found — workflow ID does not exist:**

```json
{
  "success": false,
  "error": "workflow not found: nonexistent-workflow-id"
}
```

**409 Conflict — workflow still running (not in terminal state):**

```json
{
  "success": false,
  "error": "workflow is still running; replay not allowed"
}
```

**409 Conflict — replay already in progress:**

```json
{
  "success": false,
  "error": "replay already in progress for workflow: <workflow_id>"
}
```

---

## 6. Integration Examples

All examples target `http://beacon-host:6969` — replace with your deployment address. The request body and expected response shape are grounded in the verified live captures in `docs/FEATURE_READINESS.md`.

### curl

```bash
curl -s -X POST http://beacon-host:6969/notify/email \
  -H "Content-Type: application/json" \
  -d '{
    "to":          "alice@example.com",
    "subject":     "Readiness check",
    "body":        "hello from beacon",
    "client_hint": "transactional"
  }'
```

Expected response (HTTP 202):

```json
{
  "success": true,
  "message": "email notification triggered",
  "data": {
    "provider":        "dev",
    "workflow_id":     "email-workflow-alice@example.com-1782351841675718000",
    "workflow_run_id": "019efc72-c98d-7a0a-b07c-24bcf2f245f3"
  }
}
```

Check for non-202 and surface the error:

```bash
HTTP_STATUS=$(curl -s -o /tmp/beacon_resp.json -w "%{http_code}" \
  -X POST http://beacon-host:6969/notify/email \
  -H "Content-Type: application/json" \
  -d '{"to":"alice@example.com","subject":"hello","body":"world"}')

if [ "$HTTP_STATUS" -ne 202 ]; then
  echo "Beacon error (HTTP $HTTP_STATUS):"
  cat /tmp/beacon_resp.json
fi
```

### Go (`net/http`)

```go
package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
)

type beaconRequest struct {
    To         string `json:"to"`
    Subject    string `json:"subject"`
    Body       string `json:"body"`
    ClientHint string `json:"client_hint,omitempty"`
}

type beaconResponseData struct {
    Provider      string `json:"provider"`
    WorkflowID    string `json:"workflow_id"`
    WorkflowRunID string `json:"workflow_run_id"`
}

type beaconResponse struct {
    Success bool               `json:"success"`
    Message string             `json:"message"`
    Data    beaconResponseData `json:"data"`
    Error   string             `json:"error"`
}

func sendEmail(beaconURL string, req beaconRequest) (*beaconResponseData, error) {
    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal: %w", err)
    }

    resp, err := http.Post(beaconURL+"/notify/email", "application/json", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("http post: %w", err)
    }
    defer resp.Body.Close()

    raw, _ := io.ReadAll(resp.Body)
    var result beaconResponse
    if err := json.Unmarshal(raw, &result); err != nil {
        return nil, fmt.Errorf("unmarshal: %w", err)
    }

    if resp.StatusCode != http.StatusAccepted {
        return nil, fmt.Errorf("beacon error %d: %s", resp.StatusCode, result.Error)
    }

    return &result.Data, nil
}

func main() {
    data, err := sendEmail("http://beacon-host:6969", beaconRequest{
        To:         "alice@example.com",
        Subject:    "Readiness check",
        Body:       "hello from beacon",
        ClientHint: "transactional",
    })
    if err != nil {
        panic(err)
    }
    fmt.Printf("workflow enqueued: id=%s run=%s provider=%s\n",
        data.WorkflowID, data.WorkflowRunID, data.Provider)
}
```

The `202` check is the signal that the workflow is durably enqueued. Save `data.WorkflowID` if your service needs to query or replay the workflow via the DLQ API.

### Python (`requests`)

```python
import requests
import sys

BEACON_URL = "http://beacon-host:6969"

def send_email(to: str, subject: str, body: str, client_hint: str = "") -> dict:
    payload = {"to": to, "subject": subject, "body": body}
    if client_hint:
        payload["client_hint"] = client_hint

    resp = requests.post(
        f"{BEACON_URL}/notify/email",
        json=payload,
        headers={"Content-Type": "application/json"},
        timeout=10,
    )

    result = resp.json()

    if resp.status_code != 202:
        raise RuntimeError(
            f"Beacon error (HTTP {resp.status_code}): {result.get('error')}"
        )

    return result["data"]


if __name__ == "__main__":
    data = send_email(
        to="alice@example.com",
        subject="Readiness check",
        body="hello from beacon",
        client_hint="transactional",
    )
    print(f"workflow enqueued: id={data['workflow_id']} "
          f"run={data['workflow_run_id']} provider={data['provider']}")
```

---

## 7. Authentication Note

**Today (current implementation):** Beacon has no application-layer authentication on `POST /notify/email`, `GET /dlq/failed`, or `POST /dlq/replay/{workflowID}`. Any request that reaches the server is accepted based solely on request validity. The deployment perimeter relies on **Cloudflare Tunnel with a Cloudflare Access policy** to restrict which callers can reach Beacon at the network layer. Downstream services must be able to reach Beacon through that tunnel.

Do not expose Beacon's port directly to the public internet without Cloudflare Access (or an equivalent network-layer control) in front of it.

**Planned (not yet implemented):** Per-service API-key authentication is designed and documented in [`docs/future-scope.md`](./future-scope.md). When implemented, each downstream service will present a `Authorization: Bearer bkn_svc_<key>` header, and Beacon will validate the key against a set of records stored in Infisical. Keys will be scoped to specific routing categories, preventing a service registered for OTP email from sending marketing bulk mail. The migration plan in `docs/future-scope.md` describes a report-only rollout mode that allows keys to be issued and verified before enforcement is turned on.

Until that work is complete, the operator-level control is: agree on `client_hint` values per service and enforce access at the Cloudflare layer.
