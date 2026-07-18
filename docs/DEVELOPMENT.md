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
   make run-server
   ```

   > **Note:** The email worker (`cmd/email_worker`) is not yet implemented.
   > `make run-email-worker` will fail until that binary is added to the repo.

4. Send a test email:
   ```bash
   curl -X POST http://localhost:6969/notify/email \
     -H "Content-Type: application/json" \
     -d '{"to":"you@example.com","subject":"Test","body":"Hello!"}'
   ```

## Make Targets

```bash
# Build (note: will fail until cmd/email_worker is added)
make build

# Build individually
make build-server

# Run (builds first if needed)
make run-server   # starts HTTP server on :6969

# Clean
make clean
```
