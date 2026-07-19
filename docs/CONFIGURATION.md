# Beacon — Configuration

Beacon is configured via environment variables. Copy `.env.example` to `.env` and fill in the
values.

## Core

| Variable | Default | Description |
|---|---|---|
| `SERVER_PORT` | `6969` | HTTP server port (server binary only) |
| `ADMIN_TOKEN` | — | Bearer token that protects `POST /admin/config/refresh`. When unset the endpoint returns `403 Forbidden` (disabled). When set, requests must include `Authorization: Bearer <ADMIN_TOKEN>`. The same value also authenticates as an unscoped admin identity on `GET /v1/dlq/failed` and `POST /v1/dlq/replay/{workflowID}` (it is rejected on `POST /v1/notify/{channel}`). |
| `CONFIG_POLL_INTERVAL` | `300` | How often (in seconds) the ConfigWatcher re-fetches control-plane config from Infisical. Set to `0` or leave unset to use the default 300 s. |

## Temporal

`TEMPORAL_ADDRESS` and `TEMPORAL_NAMESPACE` are read by the Temporal Go SDK's `envconfig.LoadDefaultClientOptions()`.

| Variable | Default | Description |
|---|---|---|
| `TEMPORAL_ADDRESS` | `localhost:7233` | Temporal server address (read by the Temporal SDK) |
| `TEMPORAL_NAMESPACE` | `default` | Temporal namespace |

## Email Worker

Each worker process serves exactly one `(channel, provider)` pair. Set one of:

| Variable | Default | Description |
|---|---|---|
| `WORKER_SPEC` | — | `<channel>-<provider>` (e.g. `email-sendgrid`). Preferred — used by the systemd template unit's `%i` instance name (`beacon-worker@email-sendgrid`). Parsed into channel + provider; the channel segment is everything before the first `-`, the provider is everything after (providers may themselves contain dashes). |
| `CHANNEL` | `email` | Channel this worker serves, when not using `WORKER_SPEC`. Only `email` is implemented. |
| `PROVIDER_NAME` | — | Which provider (config map key) this worker serves, when not using `WORKER_SPEC`. Defaults to the `is_default` provider, or the sole provider if only one exists. |

`WORKER_SPEC` takes priority over `CHANNEL`/`PROVIDER_NAME` when both are set.

## Control Plane (Infisical — production)

Beacon loads three kinds of control-plane objects from [Infisical](https://infisical.com/):

| Secret path | Contents | Validated by |
|---|---|---|
| `/beacon/providers/email` | One secret per SMTP provider (`SMTPClientConfig`) | `ValidateConfig` |
| `/beacon/tenants` | One secret per tenant (a team/product owning services) | `ValidateTenantConfig` |
| `/beacon/services` | One secret per registered calling service, including API keys and per-channel policy | `ValidateServiceConfig` + `ValidateBundleRefs` |

> **Renamed from v1**: this path was previously `/beacon/smtp`. If you are migrating an existing
> Infisical project, move (don't just add) the provider secrets to `/beacon/providers/email`.

See [`infisical-example.json`](../infisical-example.json) for the full JSON shape of all three.

At startup the initial fetch is retried with bounded backoff — up to 5 attempts over ~31 s —
before the process gives up. On a failed background refresh (via `ConfigWatcher` or
`POST /admin/config/refresh`), Beacon reverts to the previously loaded config rather than running
with a partial or empty one (fail-closed).

### `/beacon/providers/email/<name>` — SMTP provider config

```json
{
  "name": "sendgrid-transactional",
  "provider": "sendgrid",
  "host": "smtp.sendgrid.net",
  "port": 587,
  "username": "apikey",
  "password": "SG.your-api-key-here",
  "auth_type": "PLAIN",
  "tls": {
    "enabled": true,
    "server_name": "smtp.sendgrid.net"
  },
  "timeout": "30s",
  "is_default": true,
  "from_address": "noreply@example.com",
  "from_name": "Example Co"
}
```

| Field | Required | Notes |
|---|---|---|
| `name` | Yes | Must match the secret key |
| `provider` | Yes | Free-text label (e.g. `sendgrid`, `mailgun`) — not used for routing; `name` is the routing key |
| `host` | Yes | Must be a valid DNS name, IP, or `localhost` |
| `port` | Yes | 1–65535 |
| `username` / `password` | Yes | SMTP credentials |
| `auth_type` | Yes | `PLAIN` or `LOGIN` only. **`OAUTH2` is rejected at validation** — not implemented. |
| `tls.enabled` | No | When `true`, `tls.server_name` is required. TLS mode is enforced on the wire (see below), not just validated. |
| `timeout` | No | Go duration string (e.g. `"30s"`); defaults to 30 s if omitted |
| `is_default` | No | Exactly one provider should be marked default per channel; workers with no `PROVIDER_NAME` resolve to it |
| `from_address` / `from_name` | No | Provider-level fallback sender identity, used only when a service's policy doesn't set its own `from` (see `channels.email.from` below, which takes priority) |

**TLS and timeout are enforced on the wire, not just validated.** When `tls.enabled` is true, the
worker's SMTP sender uses implicit TLS on port 465 and mandatory STARTTLS on any other port
(rejecting the connection if the server doesn't offer STARTTLS); `tls.server_name` is used for
certificate verification. `timeout` bounds the SMTP dial/write.

**Removed from v1**: the `categories` array (category-based routing) no longer exists on this
object — provider selection is via each service's `channels.email.providers` allowlist and
`default_provider`, not provider-declared categories.

### `/beacon/tenants/<tenant>` — tenant metadata

```json
{
  "tenant": "payments",
  "name": "Payments Team",
  "owner": "payments-oncall@example.com"
}
```

`tenant` is required and must match the secret key; `name` and `owner` are informational. Every
service's `tenant` field must reference an existing tenant here, or the bundle load fails
validation.

### `/beacon/services/<service>` — registered calling service

```json
{
  "service": "billing-api",
  "tenant": "payments",
  "enabled": true,
  "keys": [
    { "id": "k1", "sha256": "<sha256-hex-of-full-api-key>", "state": "active" }
  ],
  "channels": {
    "email": {
      "providers": ["sendgrid-transactional"],
      "default_provider": "sendgrid-transactional",
      "from": {
        "address": "billing@example.com",
        "name": "Billing"
      },
      "rate": {
        "rpm": 60,
        "daily": 5000
      }
    }
  }
}
```

| Field | Notes |
|---|---|
| `service` | Must match the secret key |
| `tenant` | Must reference an existing `/beacon/tenants/<tenant>` entry |
| `enabled` | When `false`, every key for this service is rejected with `403 service disabled` |
| `keys[].id` | `^[a-z0-9-]{1,32}$` |
| `keys[].sha256` | 64 hex chars — SHA-256 of the full API key handed to the service, never the plaintext key |
| `keys[].state` | `"active"` keys are looked up; any other value is ignored at auth time (kept for audit/history) |
| `channels.email.providers` | Allowlist; the request's optional `provider` field must be a member or the request is rejected (403) |
| `channels.email.default_provider` | Used when the request omits `provider`; must be a member of `providers` |
| `channels.email.from` | Policy-locked sender identity injected into every request from this service — the request body can never set `From` |
| `channels.email.rate.rpm` / `.daily` | Per-service, per-channel rate limits (≥ 1 each); see `docs/API.md` for limiter semantics |

**Removed from v1**: the `client_hint` request field and its provider-category matching are gone.
Provider selection is now explicit allowlist + default binding per service, set here.

### API key format, hashing, and rotation

Full keys handed to calling services have the form:

```
bk_<keyid>_<secret>
```

Only `sha256(full_key)` is ever stored (in `keys[].sha256`); Beacon never persists or logs the
plaintext key. Authentication does a lookup by hash under a read lock — the presented key is
never compared as a string.

Two **active** key entries on one service is how zero-downtime rotation works:

1. Mint a new secret, compute its SHA-256, and add it as a second entry with a new `id` (e.g.
   `k2`) and `"state": "active"`, alongside the existing `k1` entry. Deploy this config (via
   `POST /admin/config/refresh` or wait for the next `CONFIG_POLL_INTERVAL` poll).
2. Roll the new full key out to the calling service; both `k1` and `k2` authenticate successfully
   during the overlap window.
3. Once the calling service confirms it is using the new key, remove the `k1` entry (or set its
   `state` to something other than `"active"`) and deploy again.

## Dev Mode (local testing, no Infisical)

Set `DEV_MODE=true` to skip Infisical and synthesize a single tenant (`dev`) and service (`dev`)
directly from environment variables, with a generous rate limit (1000 rpm / 100000 daily).

> **Note:** When `DEV_MODE=true`, `POST /admin/config/refresh` returns `503 Service Unavailable` —
> there is no control plane to refresh against.

| Variable | Default | Description |
|---|---|---|
| `DEV_MODE` | `false` | Set to `true` to enable dev mode |
| `DEV_API_KEY` | — | **Required** when `DEV_MODE=true`. The full API key for the synthesized `dev` service — callers send `Authorization: Bearer <DEV_API_KEY>`. Only its SHA-256 is kept in memory. |
| `DEV_SMTP_HOST` | — | SMTP host (required in dev mode, e.g. `localhost`) |
| `DEV_SMTP_PORT` | `587` | SMTP port |
| `DEV_SMTP_NAME` | `dev` | Internal provider name for this entry; also the synthesized service's allowed/default provider |
| `DEV_SMTP_PROVIDER` | value of `DEV_SMTP_NAME` | Provider label used in responses and task-queue routing |
| `DEV_SMTP_AUTH_TYPE` | `PLAIN` | Auth type: `PLAIN` or `LOGIN` (not validated in dev mode, but `OAUTH2` is rejected in production) |
| `DEV_SMTP_FROM` | value of `DEV_SMTP_USERNAME`, or `noreply@beacon.local` | From address for outbound email |
| `DEV_SMTP_FROM_NAME` | `Beacon` | Display name for outbound email |
| `DEV_SMTP_USERNAME` | — | SMTP username |
| `DEV_SMTP_PASSWORD` | — | SMTP password |

## Control Plane Connection (Infisical)

| Variable | Description |
|---|---|
| `INFISICAL_ADDR` | Infisical instance URL (defaults to `http://localhost:8000`) |
| `INFISICAL_PROJECT_ID` | Project ID |
| `INFISICAL_ENVIRONMENT` | Environment (e.g. `dev`, `prod`; defaults to `prod`) |

### Authentication

Beacon detects which credentials to use in this priority order:

1. **Machine Identity** — `INFISICAL_CLIENT_ID` + `INFISICAL_CLIENT_SECRET` (recommended for production)
2. **API Key** — `INFISICAL_API_KEY`

If multiple sets of credentials are present, the highest-priority method wins.

| Variable | Description |
|---|---|
| `INFISICAL_CLIENT_ID` | Machine identity client ID |
| `INFISICAL_CLIENT_SECRET` | Machine identity client secret (required with `INFISICAL_CLIENT_ID`) |
| `INFISICAL_API_KEY` | Infisical API key (used when machine identity vars are absent) |
