# Beacon

Beacon is an async notification service built in Go. It currently supports email delivery via SMTP, with Temporal handling workflow orchestration, retries, and fault tolerance.

---

## Documentation

| Document | Description |
|---|---|
| [API Reference](docs/API.md) | All endpoints with request/response shapes and status codes |
| [Architecture Overview](docs/ARCHITECTURE.md) | Component diagram, request lifecycle, tech stack |
| [Configuration Reference](docs/CONFIGURATION.md) | Every environment variable with defaults and descriptions |
| [Deployment Guide](docs/DEPLOYMENT.md) | Docker Compose and systemd deployment instructions |

---

## Features

- **Async email delivery** via Temporal workflows with automatic retries
- **Per-service API-key auth** — every `/v1/*` request requires `Authorization: Bearer bk_<keyid>_<secret>` (or `X-API-Key`); each service is bound to an allowlist of channels/providers, a policy-locked sender identity, and its own rate limits
- **Idempotent sends** — an optional `Idempotency-Key` header deduplicates retried requests
- **Multi-provider SMTP routing** — configure multiple providers in Infisical; each service picks from its allowed providers or falls back to its configured default
- **Config watcher** — hot-reloads the control plane (providers, tenants, services) from Infisical without restart (`CONFIG_POLL_INTERVAL`)
- **Dead Letter Queue** — query failed workflows (`GET /v1/dlq/failed`) and replay them (`POST /v1/dlq/replay/{workflowID}`), tenant-scoped for non-admin callers
- **Admin config refresh** — manually trigger a reload via `POST /admin/config/refresh` (requires `ADMIN_TOKEN`)
- **Health probes** — `/healthz/live` and `/healthz/ready` for liveness and readiness checks

---

## Quick Start

1. Start Temporal:
   ```bash
   temporal server start-dev
   ```

2. Set up your environment:
   ```bash
   cp .env.example .env
   # Set DEV_MODE=true, fill in DEV_SMTP_* vars, and set DEV_API_KEY
   # (the synthesized "dev" service's API key — see docs/CONFIGURATION.md)
   ```

3. Run the services:
   ```bash
   make run-server &
   make run-email-worker
   ```

4. Send a test email:
   ```bash
   curl -X POST http://localhost:6969/v1/notify/email \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer $DEV_API_KEY" \
     -d '{"to":"you@example.com","subject":"Test","body":"Hello!"}'
   ```

Both the HTTP server and the email worker must be running for delivery to work.

For a fully local smoke test (no real Infisical instance needed), use the mock test script:
```bash
bash scripts/test-local.sh
```

---

## Make Targets

```bash
# Build both binaries into bin/
make build

# Build individually
make build-server
make build-email-worker

# Run (builds first if needed)
make run-server        # starts HTTP server on :6969
make run-email-worker  # starts Temporal worker

# Clean
make clean
```

---

## Testing

```bash
# Unit tests
make test

# Coverage gate (requires ≥90% across internal/ and utils)
make cover

# HTML coverage report written to coverage.html
make cover-html

# Integration tests (require a reachable Temporal at localhost:7233)
make test-integration
```

To smoke-test a running deployment's endpoints (health, notify, DLQ, admin):
```bash
bash scripts/readiness-check.sh
```

---

## Prerequisites

- Go 1.24+
- [Temporal](https://learn.temporal.io/getting_started/go/dev_environment/) running at `localhost:7233`
- An SMTP provider or dev mode (`DEV_MODE=true`) with local SMTP vars
- [Infisical](docs/DEPLOYMENT.md#4-infisical-setup) for production SMTP secret management (optional in dev mode)
