# Beacon — Configuration

Beacon is configured via environment variables. Copy `.env.example` to `.env` and fill in the values.

## Core

| Variable | Default | Description |
|---|---|---|
| `SERVER_PORT` | `6969` | HTTP server port |
| `CONFIG_POLL_INTERVAL` | `300` | Seconds between automatic Infisical config refreshes |
| `ADMIN_TOKEN` | — | Bearer token required for `POST /admin/config/refresh`. When unset, the endpoint returns `403 Forbidden` for all requests. |

## Temporal

| Variable | Default | Description |
|---|---|---|
| `TEMPORAL_ADDRESS` | `localhost:7233` | Temporal server address |
| `TEMPORAL_NAMESPACE` | `default` | Temporal namespace |

## SMTP Config (via Infisical — production)

Beacon loads SMTP provider configuration from [Infisical](https://infisical.com/) at path `/beacon/smtp`. See `infisical-example.json` for the expected JSON shape.

| Variable | Description |
|---|---|
| `INFISICAL_ADDR` | Infisical instance URL |
| `INFISICAL_PROJECT_ID` | Project ID |
| `INFISICAL_ENVIRONMENT` | Environment (e.g. `dev`, `prod`) |
| `INFISICAL_CLIENT_ID` | Machine identity client ID |
| `INFISICAL_CLIENT_SECRET` | Machine identity client secret |

## SMTP Config (dev mode — local testing)

Set `DEV_MODE=true` to skip Infisical and load SMTP config directly from env vars.

| Variable | Description |
|---|---|
| `DEV_MODE` | Set to `true` to enable dev mode |
| `DEV_SMTP_HOST` | SMTP host (e.g. `smtp.sendgrid.net`) |
| `DEV_SMTP_PORT` | SMTP port (e.g. `587`) |
| `DEV_SMTP_USERNAME` | SMTP username |
| `DEV_SMTP_PASSWORD` | SMTP password |
| `DEV_SMTP_AUTH_TYPE` | Auth type: `PLAIN`, `LOGIN`, or `OAUTH2` |
