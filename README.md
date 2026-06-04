# Beacon

Beacon is an async notification service built in Go. It currently supports email delivery via SMTP, with Temporal handling workflow orchestration, retries, and fault tolerance.

---

## Documentation

- [Architecture Overview](docs/ARCHITECTURE.md)
- [Configuration Reference](docs/CONFIGURATION.md)
- [API Reference](docs/API.md)
- [Development Guide](docs/DEVELOPMENT.md)

---

## Features

- **Async email delivery** via Temporal workflows with automatic retries
- **Multi-provider SMTP routing** — configure multiple providers in Infisical and route by category using `client_hint`
- **Config watcher** — hot-reloads provider configs from Infisical without restart (`CONFIG_POLL_INTERVAL`)
- **Dead Letter Queue** — query failed workflows (`GET /dlq/failed`) and replay them (`POST /dlq/replay/{workflowID}`)
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
   # Set DEV_MODE=true and fill in DEV_SMTP_* vars
   ```

3. Run the services:
   ```bash
   make run-server &
   make run-email-worker
   ```

4. Send a test email:
   ```bash
   curl -X POST http://localhost:6969/notify/email \
     -H "Content-Type: application/json" \
     -d '{"to":"you@example.com","subject":"Test","body":"Hello!"}'
   ```

Both the HTTP server and the email worker must be running for delivery to work.

For a fully local smoke test (no real Infisical instance needed), use the mock test script:
```bash
bash scripts/test-local.sh
```

For all available make targets, see the [Development Guide](docs/DEVELOPMENT.md).

---

## Prerequisites

- Go 1.24+
- [Temporal](https://learn.temporal.io/getting_started/go/dev_environment/) running at `localhost:7233`
- An SMTP provider or dev mode (`DEV_MODE=true`) with local SMTP vars
- [Infisical](docs/infisical/INFISICAL_QUICKSTART.md) for production SMTP secret management (optional in dev mode)
