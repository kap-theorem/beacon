# Beacon

Beacon is an async notification service built in Go. It currently supports email delivery via SMTP, with Temporal handling workflow orchestration, retries, and fault tolerance.

---

## Documentation

| Document | Description |
|---|---|
| [API Reference](docs/API.md) | All endpoints with request/response shapes and status codes |
| [Architecture Overview](docs/ARCHITECTURE.md) | Component diagram, request lifecycle, tech stack |
| [Configuration Reference](docs/CONFIGURATION.md) | Every environment variable with defaults and descriptions |
| [Development Guide](docs/DEVELOPMENT.md) | Local setup, build targets, testing workflow |
| [Deployment Guide](docs/DEPLOYMENT.md) | Docker Compose and systemd deployment instructions |
| [Integration Guide](docs/INTEGRATION.md) | How upstream services call Beacon |
| [Feature Readiness Matrix](docs/FEATURE_READINESS.md) | Verified endpoint I/O and doc-discrepancy findings |
| [Future Scope](docs/future-scope.md) | Planned features and known limitations |

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

---

## Prerequisites

- Go 1.24+
- [Temporal](https://learn.temporal.io/getting_started/go/dev_environment/) running at `localhost:7233`
- An SMTP provider or dev mode (`DEV_MODE=true`) with local SMTP vars
