# Beacon

Beacon is an async notification service built in Go. It currently supports email delivery via SMTP, with Temporal handling workflow orchestration, retries, and fault tolerance.

---

## Documentation

- [Architecture Overview](docs/ARCHITECTURE.md)
- [Configuration Reference](docs/CONFIGURATION.md)
- [API Reference](docs/API.md)
- [Development Guide](docs/DEVELOPMENT.md)

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
   make run-http &
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

## Prerequisites

- Go 1.24+
- [Temporal](https://learn.temporal.io/getting_started/go/dev_environment/) running at `localhost:7233`
- An SMTP provider or dev mode (`DEV_MODE=true`) with local SMTP vars
