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

## Response envelope

All non-health endpoints return JSON using the same envelope:

```json
{
  "success": true | false,
  "message": "optional human-readable summary",
  "data":    { ... },      // present on success
  "error":   "..."         // present on failure
}
```

Health endpoints return plain text (`ok` / `ready` / `not ready`).

---

## Endpoints

---

### POST /notify/email

Dispatch an email via a Temporal workflow.

#### Request body

| Field | Type | Required | Description |
|---|---|---|---|
| `to` | string (email) | Yes | Recipient address |
| `subject` | string | Yes | Email subject line |
| `body` | string | No | Plain-text email body |
| `client_hint` | string | No | Routing hint — provider category or exact provider name. Uses default provider when omitted. |

```json
{
  "to": "user@example.com",
  "subject": "Your order has shipped",
  "body": "Hi! Your package is on its way.",
  "client_hint": "transactional"
}
```

#### Responses

| Status | Meaning |
|---|---|
| 202 | Workflow dispatched — `data.workflow_id` returned |
| 400 | Missing required fields or routing error |
| 405 | Method not allowed |
| 500 | Temporal dispatch failed |
| 503 | Temporal client not available |

**202 response `data` object**:

| Field | Type | Description |
|---|---|---|
| `workflow_id` | string | Temporal workflow ID |
| `workflow_run_id` | string | Temporal run ID |
| `provider` | string | Email provider selected for this request |

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

---

## DLQ

### GET /dlq/failed

List closed Temporal workflow executions that ended in a terminal failure state
(`Failed`, `TimedOut`, or `Canceled`).

#### Query parameters

| Parameter | Type | Default | Description |
|---|---|---|---|
| `status` | string | *(all)* | Filter by status: `Failed`, `TimedOut`, or `Canceled` |
| `provider` | string | *(all)* | Filter by provider name |
| `from` | string (RFC 3339) | *(unset)* | Inclusive start of close-time window |
| `to` | string (RFC 3339) | *(unset)* | Inclusive end of close-time window |
| `limit` | integer | 20 | Max results (capped at 100) |
| `offset` | integer | 0 | Pagination offset |

#### Responses

| Status | Meaning |
|---|---|
| 200 | Query successful |
| 405 | Method not allowed |
| 500 | Temporal query failed |
| 503 | Temporal client not available |

**200 response `data` object**:

| Field | Type | Description |
|---|---|---|
| `failures` | array | Array of `FailedNotification` objects |
| `count` | integer | Number of items returned |

**`FailedNotification` object**:

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

```json
{
  "success": true,
  "data": {
    "failures": [
      {
        "workflow_id": "email-workflow-user@example.com-1748900000000000000",
        "run_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
        "recipient": "user@example.com",
        "subject": "Your order has shipped",
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

### POST /dlq/replay/{workflowID}

Reads the original `EmailMessage` from a failed workflow's Temporal history and dispatches
a **new** workflow execution with a fresh ID. The original execution is preserved.

Only workflows in a terminal state (`Failed`, `TimedOut`, `Canceled`) can be replayed.

#### Path parameter

| Parameter | Type | Description |
|---|---|---|
| `workflowID` | string | Temporal workflow ID of the failed execution |

#### Responses

| Status | Meaning |
|---|---|
| 202 | Replay dispatched — new `workflow_id` returned |
| 400 | `workflowID` missing from path |
| 404 | Workflow not found in Temporal |
| 405 | Method not allowed |
| 409 | Workflow is still running — replay not allowed |
| 500 | Replay dispatch failed |
| 503 | Temporal client not available |

**202 response `data` object** (`ReplayResult`):

| Field | Type | Description |
|---|---|---|
| `new_workflow_id` | string | New Temporal workflow ID |
| `new_run_id` | string | New Temporal run ID |
| `original_workflow_id` | string | The ID passed in the path |
| `provider` | string | Provider used for the replay |

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

---

### POST /admin/config/refresh

Forces an immediate re-fetch of all SMTP configurations from Infisical and reloads the
in-memory `EmailClientRegistry`. Useful for propagating secret updates without waiting
for the next background poll (default 300 s).

**Requires**: `Authorization: Bearer <ADMIN_TOKEN>`

#### Responses

| Status | Meaning |
|---|---|
| 200 | Config reloaded successfully |
| 401 | Token missing or does not match `ADMIN_TOKEN` |
| 403 | Admin endpoint disabled (`ADMIN_TOKEN` not set) |
| 405 | Method not allowed |
| 500 | Infisical fetch or registry reload failed |

**200 response `data` object**:

| Field | Type | Description |
|---|---|---|
| `revision` | integer | Infisical config revision after reload |
| `providers` | string[] | Provider names active after reload |

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

### GET /healthz/live

Liveness probe. Returns HTTP 200 `ok` as long as the process is running.
Safe to call frequently — no external dependencies checked.

| Status | Body |
|---|---|
| 200 | `ok` |

---

### GET /healthz/ready

Readiness probe. Returns HTTP 200 once startup is complete (config loaded, registry built).
Returns HTTP 503 during startup or after a fatal initialization failure.

| Status | Body |
|---|---|
| 200 | `ready` |
| 503 | `not ready` |

---

## Error responses

All error responses follow the envelope format:

```json
{
  "success": false,
  "error": "human-readable description"
}
```

Common error messages:

| HTTP | `error` value | Cause |
|---|---|---|
| 400 | `missing required field: to` / `missing required field: subject` | Required field absent in email request |
| 400 | `routing error: ...` | `client_hint` could not be resolved to a provider |
| 400 | `invalid request body` | Malformed JSON |
| 400 | `workflow ID is required` | Empty path segment in `/dlq/replay/` |
| 401 | `unauthorized` | Admin token mismatch |
| 403 | `admin endpoint disabled` | `ADMIN_TOKEN` env var not set |
| 404 | `workflow not found: <id>` | Workflow ID does not exist in Temporal |
| 405 | `method not allowed` | Wrong HTTP method |
| 409 | `workflow is still running; replay not allowed` | Non-terminal workflow state |
| 500 | `failed to trigger email notification` | Temporal dispatch error |
| 500 | `failed to query workflow failures` | Temporal history query error |
| 500 | `replay failed` | Temporal replay dispatch error |
| 503 | `temporal service not available` | Temporal client failed to connect at startup |
