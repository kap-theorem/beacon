# Beacon — Configuration

Beacon is configured via environment variables. Copy `.env.example` to `.env` and fill in the values.

## Core

| Variable | Default | Description |
|---|---|---|
| `SERVER_PORT` | `6969` | HTTP server port (server binary only) |
| `ADMIN_TOKEN` | — | Bearer token that protects `POST /admin/config/refresh`. When unset the endpoint returns `403 Forbidden` (disabled). When set, requests must include `Authorization: Bearer <ADMIN_TOKEN>`. |
| `CONFIG_POLL_INTERVAL` | `300` | How often (in seconds) the ConfigWatcher re-fetches SMTP config from Infisical. Set to `0` or leave unset to use the default 300 s. |
| `PROVIDER_NAME` | — | Worker only. SMTP provider key to serve (e.g. `mailgun-payments`). If unset, the provider marked `is_default: true` is used; if only one provider exists it is used automatically. |

> Note: `EMAIL_NOTIFIER_TASK_QUEUE` is **not** read by the codebase. The task queue name is derived at runtime as `email-<providerName>-queue` by `notifier.TaskQueueFor()`. Do not set this variable.

## Temporal

`TEMPORAL_ADDRESS` and `TEMPORAL_NAMESPACE` are read by the Temporal Go SDK's `envconfig.LoadDefaultClientOptions()`.

| Variable | Default | Description |
|---|---|---|
| `TEMPORAL_ADDRESS` | `localhost:7233` | Temporal server address |
| `TEMPORAL_NAMESPACE` | `default` | Temporal namespace |

## SMTP Config (via Infisical — production)

Beacon loads SMTP provider configuration from [Infisical](https://infisical.com/) at path `/beacon/smtp`. See `infisical-example.json` for the expected JSON shape.

The service supports three Infisical authentication methods (evaluated in this order of preference):

| Variable | Description |
|---|---|
| `INFISICAL_ADDR` | Infisical instance URL (defaults to `http://localhost:8000`) |
| `INFISICAL_PROJECT_ID` | Project ID |
| `INFISICAL_ENVIRONMENT` | Environment (e.g. `dev`, `prod`; defaults to `prod`) |
| `INFISICAL_CLIENT_ID` | Machine identity client ID (preferred auth method) |
| `INFISICAL_CLIENT_SECRET` | Machine identity client secret (required with `INFISICAL_CLIENT_ID`) |
| `INFISICAL_API_KEY` | Infisical API key (legacy auth, used when machine identity vars are absent) |
| `INFISICAL_TOKEN` | Legacy service token (used when neither machine identity nor API key is present) |

## SMTP Config (dev mode — local testing)

Set `DEV_MODE=true` to skip Infisical and load SMTP config directly from env vars. `DEV_SMTP_HOST` is required when `DEV_MODE=true`.

| Variable | Default | Description |
|---|---|---|
| `DEV_MODE` | `false` | Set to `true` to enable dev mode |
| `DEV_SMTP_HOST` | — | SMTP host (required in dev mode, e.g. `localhost`) |
| `DEV_SMTP_PORT` | `587` | SMTP port |
| `DEV_SMTP_NAME` | `dev` | Internal name for this provider entry |
| `DEV_SMTP_PROVIDER` | value of `DEV_SMTP_NAME` | Provider label used in responses and task queue routing |
| `DEV_SMTP_AUTH_TYPE` | `PLAIN` | Auth type: `PLAIN`, `LOGIN`, or `OAUTH2` |
| `DEV_SMTP_FROM` | value of `DEV_SMTP_USERNAME`, or `noreply@beacon.local` | From address for outbound email |
| `DEV_SMTP_FROM_NAME` | `Beacon` | Display name for outbound email |
| `DEV_SMTP_USERNAME` | — | SMTP username |
| `DEV_SMTP_PASSWORD` | — | SMTP password |
