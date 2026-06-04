# Beacon — Configuration

Beacon is configured via environment variables. Copy `.env.example` to `.env` and fill in the values.

## Core

| Variable | Default | Description |
|---|---|---|
| `SERVER_PORT` | `6969` | HTTP server port |
| `CONFIG_POLL_INTERVAL` | `300` | How often (in seconds) the ConfigWatcher polls Infisical for updated SMTP configs |
| `ADMIN_TOKEN` | — | Bearer token for `POST /admin/config/refresh`; leave unset to disable the endpoint |

## Temporal

| Variable | Default | Description |
|---|---|---|
| `TEMPORAL_ADDRESS` | `localhost:7233` | Temporal server address (read by the Temporal SDK) |
| `TEMPORAL_NAMESPACE` | `default` | Temporal namespace |

## Email Worker

| Variable | Default | Description |
|---|---|---|
| `PROVIDER_NAME` | — | Which SMTP provider this worker serves (config map key, e.g. `mailgun-payments`). Defaults to the `is_default` provider, or auto-selected if only one exists. |

## SMTP Config (via Infisical — production)

Beacon loads SMTP provider configuration from [Infisical](https://infisical.com/) at path `/beacon/smtp`. See [`infisical-example.json`](../infisical-example.json) for the expected JSON shape.

### Connection

| Variable | Description |
|---|---|
| `INFISICAL_ADDR` | Infisical instance URL |
| `INFISICAL_PROJECT_ID` | Project ID |
| `INFISICAL_ENVIRONMENT` | Environment (e.g. `dev`, `prod`) |

### Authentication

Beacon detects which credentials to use in this priority order:

1. **Machine Identity** — `INFISICAL_CLIENT_ID` + `INFISICAL_CLIENT_SECRET` (recommended for production)
2. **API Key** — `INFISICAL_API_KEY`
3. **Legacy Token** — `INFISICAL_TOKEN`

If multiple sets of credentials are present, the highest-priority method wins.

| Variable | Description |
|---|---|
| `INFISICAL_CLIENT_ID` | Machine identity client ID |
| `INFISICAL_CLIENT_SECRET` | Machine identity client secret |
| `INFISICAL_API_KEY` | Infisical API key (alternative to machine identity) |
| `INFISICAL_TOKEN` | Legacy API token (backward compatible) |

## SMTP Config (dev mode — local testing)

Set `DEV_MODE=true` to skip Infisical and load SMTP config directly from env vars.

> **Note:** When `DEV_MODE=true`, the server starts without Infisical credentials. Calling `POST /admin/config/refresh` will attempt to reach Infisical and fail — config refresh cannot be used in dev mode.

| Variable | Description |
|---|---|
| `DEV_MODE` | Set to `true` to enable dev mode |
| `DEV_SMTP_HOST` | SMTP host (e.g. `smtp.sendgrid.net`) |
| `DEV_SMTP_PORT` | SMTP port (e.g. `587`) |
| `DEV_SMTP_USERNAME` | SMTP username |
| `DEV_SMTP_PASSWORD` | SMTP password |
| `DEV_SMTP_AUTH_TYPE` | Auth type: `PLAIN`, `LOGIN`, or `OAUTH2` |
| `DEV_SMTP_NAME` | Provider name (defaults to `DEV_SMTP_HOST` value) |
| `DEV_SMTP_PROVIDER` | Provider type identifier (defaults to `DEV_SMTP_NAME`) |
| `DEV_SMTP_FROM` | From address (defaults to `DEV_SMTP_USERNAME` or `noreply@beacon.local`) |
| `DEV_SMTP_FROM_NAME` | From display name (defaults to `Beacon`) |
