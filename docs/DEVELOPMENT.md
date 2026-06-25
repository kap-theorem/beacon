# Beacon — Development Guide

## Prerequisites

- Go 1.24+
- A running [Temporal server](https://learn.temporal.io/getting_started/go/dev_environment/) (`localhost:7233` by default)
- An SMTP provider (SendGrid, Gmail SMTP, etc.) — or dev mode for local testing

## Local Development Setup

1. Start Temporal (using the Temporal CLI):
   ```bash
   temporal server start-dev
   ```

2. Create a `.env` file:
   ```bash
   cp .env.example .env
   # Set DEV_MODE=true and fill in DEV_SMTP_* vars
   ```

3. Build and run:
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

## Building and Running

```bash
# Build both binaries into bin/
make build

# Build individually
make build-server
make build-email-worker

# Run
make run-server        # starts HTTP server
make run-email-worker  # starts Temporal worker

# Clean
make clean
```

Both the HTTP server and the email worker must be running for email delivery to work.

## Testing

```bash
# All unit tests
make test

# Coverage gate — enforces ≥90% across internal/ and utils
make cover

# HTML coverage report written to coverage.html
make cover-html

# Integration tests — require a reachable Temporal at localhost:7233
make test-integration
```
