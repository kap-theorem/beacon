# Beacon — Deployment Runbook

**Target environment**: Self-hosted home server
**Stack**: Self-hosted Temporal cluster (Postgres-backed) + per-provider Beacon workers + Beacon server + Cloudflare Tunnel
**Last reconciled against code**: 2026-06-25 (branch `fix/known-issues`)

---

## Table of Contents

1. [Overview and Topology](#1-overview-and-topology)
2. [Prerequisites](#2-prerequisites)
3. [Temporal Cluster Bring-Up](#3-temporal-cluster-bring-up)
4. [Infisical Setup — SMTP Providers and Routing](#4-infisical-setup--smtp-providers-and-routing)
5. [Beacon Server and Per-Provider Workers](#5-beacon-server-and-per-provider-workers)
6. [Cloudflare Tunnel](#6-cloudflare-tunnel)
7. [Onboarding a Downstream Service](#7-onboarding-a-downstream-service)
8. [Security Model](#8-security-model)
9. [Observability](#9-observability)
10. [Upgrade, Rollback, Backup, and Scaling](#10-upgrade-rollback-backup-and-scaling)

---

## 1. Overview and Topology

Beacon is an async email notification service. Downstream services submit notification requests over HTTPS through a Cloudflare Tunnel; the HTTP server resolves the request to an SMTP provider and enqueues a Temporal workflow on **that provider's own task queue**; the worker for that provider executes SMTP delivery.

### Topology

```
Downstream services (auth-service, order-service, marketing-service, ...)
        |
        | HTTPS  https://beacon.example.com
        v
+-------------------------------------------------------------+
|  Cloudflare Tunnel (cloudflared)                            |
|    /healthz/*    -> public (no Access policy)               |
|    /notify/email -> Cloudflare Access (service token)       |
|    /dlq/*        -> Cloudflare Access (service token)       |
|    everything else (incl. /admin/*) -> HTTP 404             |
+-------------------------------------------------------------+
        |
        | http://beacon-server:6969  (private network only)
        v
+-----------------------------------+
|  Beacon HTTP Server               |
|  cmd/server   port 6969           |
|  resolves client_hint -> provider |
|  -> task queue email-<name>-queue |
+-----------------------------------+
        |
        | gRPC :7233  (start workflow on the provider's task queue)
        v
+---------------------------+        +---------------------------+
|  Temporal Server          |<------>|  PostgreSQL :5432         |
|  temporalio/auto-setup    |        |  (temporal persistence)   |
|  :7233                    |        +---------------------------+
+---------------------------+
        |
        |  ONE task queue per provider name
        |
   email-sendgrid-              email-mailgun-
   transactional-queue          marketing-queue
        |                            |
        v                            v
+---------------------------+   +---------------------------+
|  beacon-worker            |   |  beacon-worker            |
|  PROVIDER_NAME=           |   |  PROVIDER_NAME=           |
|  sendgrid-transactional   |   |  mailgun-marketing        |
+---------------------------+   +---------------------------+
        |                            |
        v                            v
   smtp.sendgrid.net            smtp.mailgun.org
```

> **Key invariant:** the task queue name is derived from the resolved provider's
> `name` field as `email-<name>-queue` (see `notifier.TaskQueueFor`). There is **no**
> single shared queue. Every provider that routing can select must have at least one
> running worker whose `PROVIDER_NAME` equals that provider's `name`, or its
> workflows will be enqueued but never delivered (they sit until the activity times
> out). See [Section 5.1](#51-how-provider-name-routing-and-the-task-queue-connect).

### Deployment Options

Two deployment models are supported and can be chosen based on your host environment:

- **Docker Compose** (`deploy/docker-compose.yml`): runs Temporal, PostgreSQL, Temporal UI, beacon-server, and each beacon-worker-\<provider\> as containers on a single Docker host. Use for straightforward single-node deployments.
- **Systemd units** (`deploy/systemd/`): runs `beacon-server` and per-provider `beacon-worker@<provider>` as native system services. Use when Temporal runs separately (e.g. as its own Compose stack) and you want OS-level service management for Beacon binaries.

---

## 2. Prerequisites

### Docker Compose path

- Docker Engine 24+ and Docker Compose v2 (`docker compose` subcommand)
- The `beacon:local` image built from the repository root (see [Building the Image](#building-the-image))

### Systemd path

- A Linux host with systemd
- Pre-built `server` and `email_worker` binaries placed at `/usr/local/bin/server` and `/usr/local/bin/email_worker`
- A dedicated `beacon` system user and group:

```bash
useradd --system --no-create-home beacon
```

- A running Temporal cluster accessible at the address you will set in `TEMPORAL_ADDRESS`

### All paths

- A Cloudflare account with Zero Trust enabled and `cloudflared` installed on the host
- An Infisical instance (self-hosted or `https://app.infisical.com`) with a project created and a machine identity provisioned

### Building the Image

```bash
# Run from the repository root
docker build -t beacon:local .
```

The multi-stage Dockerfile (`golang:1.24` builder → `gcr.io/distroless/static-debian12:nonroot` runtime) compiles both `server` and `email_worker` binaries from `cmd/server` and `cmd/email_worker` and copies them into the minimal runtime image. The image `ENTRYPOINT` defaults to `/usr/local/bin/server`; the worker is selected by overriding `command` (the Compose file does this for you).

---

## 3. Temporal Cluster Bring-Up

### Start the cluster

```bash
docker compose -f deploy/docker-compose.yml up -d postgresql temporal temporal-ui
```

This starts three services:

- `postgresql` — PostgreSQL 15 on the `postgres-data` named volume (user/pass/db all `temporal`)
- `temporal` — `temporalio/auto-setup:1.25.2`, initialises the schema automatically on first boot, gRPC on `:7233`
- `temporal-ui` — `temporalio/ui:2.31.2`, web UI on `:8080`

The `temporal` service depends on `postgresql` being healthy (`pg_isready` passes) before starting. The first boot may take 30–60 seconds while `auto-setup` runs schema migrations and creates the `default` namespace (`DEFAULT_NAMESPACE: default`).

### Verify the cluster

```bash
# Check namespace listing (requires temporal CLI installed on the host)
temporal operator namespace list

# Or describe the auto-created default namespace
temporal operator namespace describe default
```

Expected output includes `Name: default` and `State: Registered`.

If the Temporal CLI is not installed on the host, use the Temporal UI at `http://<host>:8080` to confirm the `default` namespace is visible.

### Dynamic configuration

The Temporal service mounts `deploy/dynamicconfig/development-sql.yaml` at `/etc/temporal/config/dynamicconfig/development-sql.yaml` (read-only). This file sets:

- `frontend.enableClientVersionCheck: true`
- `limit.maxIDLength: 1000`
- `system.advancedVisibilityWritingMode: "off"` (appropriate for PostgreSQL without Elasticsearch)

No changes to this file are needed for a standard deployment.

---

## 4. Infisical Setup — SMTP Providers and Routing

Beacon reads SMTP provider configuration from Infisical at the path `/beacon/smtp`. Each secret under that path is one SMTP provider config (a JSON-encoded `SMTPClientConfig`).

### Secret structure

Each secret has:
- **Key**: the provider name. By convention, set the Infisical secret key equal to the JSON `name` field below (e.g. `sendgrid-transactional`).
- **Value**: a JSON object matching the structure shown in `infisical-example.json`.

Example secret value for the key `sendgrid-transactional`:

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
  "max_retries": 3,
  "max_per_hour": 0,
  "categories": ["transactional", "otp"],
  "from_address": "noreply@example.com",
  "from_name": "Example App",
  "is_default": true
}
```

### Field reference

| Field | Required | Purpose |
|---|---|---|
| `name` | Yes | The internal provider key. **This is the value Beacon keys everything off** — routing returns it, the task queue is `email-<name>-queue`, and a worker's `PROVIDER_NAME` must equal it. Returned in the `202` response as `provider`. |
| `provider` | Yes | A free-form provider label (e.g. `sendgrid`, `mailgun`). Validated as required but **not** used for routing or queue naming. |
| `host`, `port` | Yes | SMTP endpoint. `port` must be 1–65535. |
| `username` | Yes (non-OAuth2) | SMTP username. |
| `password` / `api_key` | Yes (at least one) | SMTP credential. Both are write-only (never serialised back out). |
| `auth_type` | Yes | One of `PLAIN`, `LOGIN`, `OAUTH2`. |
| `tls` | No | `{ "enabled": true, "server_name": "..." }`. `server_name` is required when `enabled` is true. |
| `timeout` | No | Go duration string (e.g. `"30s"`). Defaults to `30s` when omitted. |
| `from_address`, `from_name` | Recommended | Used to build the `From` header. **If `from_address` is empty the `From` header is empty and most SMTP providers reject the message** — always set it for production providers. |
| `categories` | No | Routing category strings this provider handles. A downstream service sends one of these as `client_hint`. `[]` (or absent) means the provider is never selected by hint. |
| `is_default` | No | When `true`, requests with no `client_hint` (or an unrecognised one) route here. |
| `max_per_hour`, `max_retries`, `rules` | No | Carried in config; rate-limit fields are not yet enforced by the delivery path. |

### Routing rules

- **Exactly one provider should have `"is_default": true`.** Beacon uses it when no `client_hint` is given, or when the given hint matches no provider's `categories`. (As a fallback, if only one provider is configured it is automatically the default even without the flag.)
- The `categories` array drives hint routing: when `POST /notify/email` includes a `client_hint`, Beacon resolves the provider whose `categories` list contains that hint.
- If a hint matches nothing **and** no default provider exists, the request returns `400 routing error: no email client for hint "..." and no default provider configured`.

### Create the secrets in Infisical

1. Open your Infisical project and navigate to **Secrets** for the target environment (e.g. `prod`).
2. Create a secret at path `/beacon/smtp` for each provider, using the provider `name` as the key and the JSON object above as the value.
3. Repeat for each additional provider (e.g. `mailgun-marketing` with `"is_default": false`).

See `infisical-example.json` in the repository root for the full structure of both a default and a non-default provider.

### Create a machine identity

1. In Infisical, go to **Access Control → Machine Identities** and create a new identity (e.g. `beacon-prod`).
2. Under the identity, create a **Universal Auth** client secret and record:
   - `CLIENT_ID` → set as `INFISICAL_CLIENT_ID`
   - `CLIENT_SECRET` → set as `INFISICAL_CLIENT_SECRET`
3. Grant the identity read access to your project for the target environment.
4. Note your project's **Project ID** from the project settings page.

> Beacon also supports legacy `INFISICAL_API_KEY` and `INFISICAL_TOKEN` auth, but machine-identity (client ID + secret) is preferred and is what the env templates use.

---

## 5. Beacon Server and Per-Provider Workers

### 5.1 How provider name, routing, and the task queue connect

This is the single most important thing to get right when standing up workers. The relationships, all keyed off the provider's `name` field:

1. The config loader keys every provider by its JSON `name` field (`bundle.SMTP[name]`). The Infisical secret key and the `provider` field are **not** used for this.
2. On `POST /notify/email`, the server resolves the request to a provider name:
   - `client_hint` matches a provider's `categories` → that provider's `name`;
   - `client_hint` empty or unmatched → the `is_default` provider's `name` (or the sole provider);
   - otherwise → `400 routing error`.
3. The resolved `name` determines the Temporal task queue: **`email-<name>-queue`** (`notifier.TaskQueueFor`).
4. A worker serves exactly one provider, chosen by the `PROVIDER_NAME` env var, which **must equal that provider's `name`**. The worker subscribes to `email-<PROVIDER_NAME>-queue`. If `PROVIDER_NAME` is unset, the worker serves the `is_default` provider (or the sole provider).

**Consequence:** for the example provider `name: "sendgrid-transactional"`, the task queue is `email-sendgrid-transactional-queue`, and its worker must run with `PROVIDER_NAME=sendgrid-transactional`. Multiple instances with the same `PROVIDER_NAME` share that one queue (this is how you scale a single provider). Different providers never share a queue.

> The variable `EMAIL_NOTIFIER_TASK_QUEUE` is **not** read by the codebase — do not set it. The queue name is always derived at runtime.

### 5.2 Docker Compose path

Copy and fill the env templates:

```bash
cp deploy/env/server.env.example deploy/env/server.env
cp deploy/env/worker.env.example deploy/env/worker.env
```

Edit `deploy/env/server.env` and set at minimum:

```bash
SERVER_PORT=6969
DEV_MODE=false
TEMPORAL_ADDRESS=temporal:7233
TEMPORAL_NAMESPACE=default
INFISICAL_ADDR=https://app.infisical.com
INFISICAL_PROJECT_ID=<your-project-id>
INFISICAL_ENVIRONMENT=prod
INFISICAL_CLIENT_ID=<your-client-id>
INFISICAL_CLIENT_SECRET=<your-client-secret>
ADMIN_TOKEN=<strong-random-value>
CONFIG_POLL_INTERVAL=300
```

Edit `deploy/env/worker.env` and set at minimum:

```bash
TEMPORAL_ADDRESS=temporal:7233
TEMPORAL_NAMESPACE=default
INFISICAL_ADDR=https://app.infisical.com
INFISICAL_PROJECT_ID=<your-project-id>
INFISICAL_ENVIRONMENT=prod
INFISICAL_CLIENT_ID=<your-client-id>
INFISICAL_CLIENT_SECRET=<your-client-secret>
DEV_MODE=false
CONFIG_POLL_INTERVAL=300
```

Do not set `PROVIDER_NAME` in `worker.env`; it is injected per-service via the `environment:` block in `docker-compose.yml`.

> **Reconcile the shipped placeholder.** `deploy/docker-compose.yml` ships one worker
> service, `beacon-worker-sendgrid`, with `PROVIDER_NAME: sendgrid`. The value `sendgrid`
> is a placeholder. Change it to match the `name` of the provider you created in Infisical
> (e.g. `sendgrid-transactional`). If `PROVIDER_NAME` does not match a configured provider
> `name`, the worker exits at startup with `provider not found`.

Start beacon-server and the worker:

```bash
docker compose -f deploy/docker-compose.yml up -d beacon-server beacon-worker-sendgrid
```

#### Adding additional providers (Compose)

Copy the `beacon-worker-sendgrid` service block in `deploy/docker-compose.yml`, give it a unique service name, and set `PROVIDER_NAME` to the new provider's `name`:

```yaml
beacon-worker-mailgun:
  image: beacon:local
  command: ["/usr/local/bin/email_worker"]
  env_file:
    - ./env/worker.env
  environment:
    PROVIDER_NAME: mailgun-marketing   # must equal the provider's "name" in Infisical
    TEMPORAL_ADDRESS: temporal:7233
  depends_on:
    - temporal
  restart: unless-stopped
```

Then bring up the new worker:

```bash
docker compose -f deploy/docker-compose.yml up -d beacon-worker-mailgun
```

Each worker polls its own provider task queue (`email-<PROVIDER_NAME>-queue`) and routes delivery through the SMTP config loaded from Infisical for that provider name.

### 5.3 Systemd path

#### Prepare configuration

```bash
# Create the config directory
mkdir -p /etc/beacon

# Copy and fill env files
cp deploy/env/server.env.example /etc/beacon/server.env
cp deploy/env/worker.env.example /etc/beacon/worker.env

# Restrict permissions — these files contain secrets
chmod 640 /etc/beacon/server.env /etc/beacon/worker.env
chown root:beacon /etc/beacon/server.env /etc/beacon/worker.env
```

Edit `/etc/beacon/server.env` and `/etc/beacon/worker.env` with the same values listed in the Compose section above. For the systemd path, set `TEMPORAL_ADDRESS` to the actual host:port where your Temporal cluster is reachable (e.g. `192.168.1.10:7233`).

#### Install and enable service units

```bash
# Copy the unit files
cp deploy/systemd/beacon-server.service /etc/systemd/system/
cp deploy/systemd/beacon-worker@.service /etc/systemd/system/

systemctl daemon-reload

# Enable and start the server
systemctl enable --now beacon-server

# Enable and start one worker per provider. The instance name (after @) becomes
# PROVIDER_NAME, so it MUST equal the provider's "name" field in Infisical.
systemctl enable --now beacon-worker@sendgrid-transactional
systemctl enable --now beacon-worker@mailgun-marketing
```

The `beacon-worker@.service` template unit sets `Environment=PROVIDER_NAME=%i`, so `beacon-worker@sendgrid-transactional` runs with `PROVIDER_NAME=sendgrid-transactional` (subscribing to `email-sendgrid-transactional-queue`). The shared `/etc/beacon/worker.env` is loaded for all instances.

Both units run as the `beacon` system user with `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, and `PrivateTmp` hardening.

#### Install and enable the cloudflared unit

```bash
cp deploy/systemd/cloudflared.service /etc/systemd/system/

systemctl daemon-reload
systemctl enable --now cloudflared
```

The cloudflared unit runs `cloudflared tunnel --config /etc/cloudflared/config.yml run` as the `cloudflared` user. Create that user and ensure the credentials file and config are readable by it:

```bash
useradd --system --no-create-home cloudflared
chown -R cloudflared:cloudflared /etc/cloudflared
```

---

## 6. Cloudflare Tunnel

### Create the tunnel

```bash
# Authenticate cloudflared with your Cloudflare account
cloudflared tunnel login

# Create a named tunnel
cloudflared tunnel create beacon
```

This produces a credentials JSON file (e.g. `~/.cloudflared/<TUNNEL_ID>.json`) and prints the `TUNNEL_ID`. Note both values.

### Route DNS

```bash
cloudflared tunnel route dns beacon beacon.example.com
```

This creates a CNAME record in your Cloudflare DNS zone pointing `beacon.example.com` to the tunnel ingress.

### Place credentials and apply configuration

```bash
# Copy the credentials file into the cloudflared config directory
cp ~/.cloudflared/<TUNNEL_ID>.json deploy/cloudflared/<TUNNEL_ID>.json
```

Edit `deploy/cloudflared/config.yml` and replace both occurrences of `<TUNNEL_ID>` with your actual tunnel ID. The file should look like:

```yaml
tunnel: abc123def456...
credentials-file: /etc/cloudflared/abc123def456....json

ingress:
  - hostname: beacon.example.com
    path: /healthz/*
    service: http://beacon-server:6969

  - hostname: beacon.example.com
    path: /notify/email
    service: http://beacon-server:6969

  - hostname: beacon.example.com
    path: /dlq/*
    service: http://beacon-server:6969

  - service: http_status:404
```

> Note the catch-all `http_status:404`: only `/healthz/*`, `/notify/email`, and `/dlq/*`
> are reachable through the tunnel. **`/admin/config/refresh` is intentionally not routed**,
> so the admin endpoint is reachable only on the private network, not via the public hostname.

For the Docker Compose path, place the credentials file and updated config under `deploy/cloudflared/` (this directory is bind-mounted into the `cloudflared` container as `/etc/cloudflared`):

```bash
docker compose -f deploy/docker-compose.yml up -d cloudflared
```

For the systemd path, copy both files to `/etc/cloudflared/` before starting the unit.

### Create a Cloudflare Access application

This step is mandatory before exposing `/notify/email` or `/dlq/*` publicly. Without it, `/notify/email` is an open relay and `/dlq/*` exposes recipient/subject metadata and re-send (replay) to any client that can reach the tunnel hostname.

1. In the Cloudflare Zero Trust dashboard, go to **Access → Applications → Add an application**.
2. Select **Self-Hosted**.
3. Set the application domain to `beacon.example.com` and the path to `/notify/email`. Repeat for `/dlq/*` (or create one application covering both paths via a wildcard if your plan supports it).
4. Under **Policies**, add a policy requiring a **Service Token**.
5. Go to **Access → Service Auth → Service Tokens** and create a token **per downstream service** that needs to call Beacon. Record the `CF-Access-Client-Id` and `CF-Access-Client-Secret`; downstream services must include both headers on every request (see [Section 7](#7-onboarding-a-downstream-service)).

The ingress rule routes `/healthz/*` without any Access policy — health probes from infrastructure tooling do not require authentication. All other reachable paths must be covered by the Access application; anything else returns 404 (catch-all).

---

## 7. Onboarding a Downstream Service

This section is the end-to-end runbook for letting a new upstream service (auth service, order service, marketing system, …) send email through Beacon. It has two halves: **operator-side Beacon configuration** and the **downstream call contract**.

For the complete API reference (every status code, response body, retry/DLQ semantics, and Go/Python client examples), see [`docs/INTEGRATION.md`](./INTEGRATION.md). This section is the deployment-facing summary plus the operator steps that `INTEGRATION.md` does not cover.

### 7.1 Beacon-side setup (operator)

Do this before the downstream team sends its first request.

**Step 1 — Decide the routing category.** Agree with the downstream team on the `client_hint` value their service will send. Examples:

| Downstream service | `client_hint` |
|---|---|
| Auth service (OTPs) | `otp` |
| Order service (receipts) | `transactional` |
| Marketing system | `marketing` |

A service that does not need category routing can omit `client_hint` entirely and rely on the default provider.

**Step 2 — Make sure a provider handles that category.** In Infisical at `/beacon/smtp`, ensure some provider's `categories` array contains the agreed hint (see [Section 4](#4-infisical-setup--smtp-providers-and-routing)). Either extend an existing provider's `categories`, or add a new provider. Config is hot-reloaded from Infisical every `CONFIG_POLL_INTERVAL` seconds (default 300); to apply it immediately, call the admin refresh endpoint from the private network:

```bash
curl -X POST http://beacon-server:6969/admin/config/refresh \
  -H "Authorization: Bearer $ADMIN_TOKEN"
# 200 -> {"success":true,"message":"config refreshed","data":{"revision":N,"providers":[...]}}
```

**Step 3 — Ensure a worker is running for that provider.** Delivery only happens if a worker with `PROVIDER_NAME=<provider name>` is polling `email-<name>-queue`. If Step 2 added a *new* provider, start a new worker for it ([Section 5.2](#52-docker-compose-path) / [5.3](#53-systemd-path)). If the category was added to an existing provider that already has a worker, no new worker is needed.

**Step 4 — Issue a Cloudflare Access service token for the service.** In **Access → Service Auth → Service Tokens**, create a token named for the downstream service (e.g. `svc-auth`). Add it to the Access policy on the `/notify/email` application (and `/dlq/*` if the service needs to query/replay its own failures). Record the two header values:

- `CF-Access-Client-Id`
- `CF-Access-Client-Secret`

**Step 5 — Hand off to the downstream team.** Provide:

- Base URL: `https://beacon.example.com`
- The `CF-Access-Client-Id` and `CF-Access-Client-Secret` for their service
- The agreed `client_hint` value (or "omit it; default provider is used")

> There is **no per-service record inside Beacon today** — the application layer does not
> know which service is calling. A service's identity and reachability are enforced entirely
> by its Cloudflare Access service token, and the provider it lands on is decided by
> `client_hint`. Planned per-service API-key auth is documented in
> [`docs/future-scope.md`](./future-scope.md); see [Section 8](#8-security-model).

### 7.2 Downstream call contract

**Endpoint:** `POST /notify/email`

**Headers (through the Cloudflare Tunnel):**

| Header | Value | Required |
|---|---|---|
| `Content-Type` | `application/json` | Yes |
| `CF-Access-Client-Id` | the service token client id | Yes (via tunnel) |
| `CF-Access-Client-Secret` | the service token client secret | Yes (via tunnel) |

The two `CF-Access-*` headers are required when calling through `https://beacon.example.com`. They are **not** required (and not understood) when calling beacon-server directly on the private network at `http://beacon-server:6969`.

**Request body:**

```json
{
  "to":          "recipient@example.com",
  "subject":     "Your verification code",
  "body":        "Your code is 482910. It expires in 10 minutes.",
  "client_hint": "otp"
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `to` | string | Yes | Recipient address. Trimmed, then validated as an RFC 5322 address. |
| `subject` | string | Yes | Must be non-empty. |
| `body` | string | No | Plain-text body. Empty if omitted. |
| `client_hint` | string | No | Routing category. Absent/unrecognised → default provider. |

**Success — `202 Accepted`:**

```json
{
  "success": true,
  "message": "email notification triggered",
  "data": {
    "provider":        "sendgrid-transactional",
    "workflow_id":     "email-workflow-recipient@example.com-1782351841675718000",
    "workflow_run_id": "019efc72-c98d-7a0a-b07c-24bcf2f245f3"
  }
}
```

`202` means the delivery workflow is durably enqueued — **not** that the email was delivered. Save `workflow_id` if the service may later need to query or replay via the DLQ API. Error responses (`400`/`405`/`500`/`503`) use `{"success": false, "error": "..."}`; see `docs/INTEGRATION.md` Section 4 for the full table.

### 7.3 End-to-end example (via the tunnel)

```bash
curl -s -X POST https://beacon.example.com/notify/email \
  -H "Content-Type: application/json" \
  -H "CF-Access-Client-Id:     ${CF_ACCESS_CLIENT_ID}" \
  -H "CF-Access-Client-Secret: ${CF_ACCESS_CLIENT_SECRET}" \
  -d '{
    "to":          "alice@example.com",
    "subject":     "Your verification code",
    "body":        "Your code is 482910.",
    "client_hint": "otp"
  }'
```

A correctly configured request returns `202` with the JSON envelope above. If the `CF-Access-*` headers are missing or invalid, Cloudflare Access returns its own challenge/403 **before** the request reaches beacon-server. If the body is malformed or routing fails, beacon-server returns a `4xx` with `{"success": false, "error": "..."}`.

---

## 8. Security Model

Beacon has **no application-layer authentication on the email or DLQ endpoints** as of this release. Specifically:

| Endpoint | App-layer auth in code | Reachable via tunnel | Protected by |
|---|---|---|---|
| `GET /healthz/live`, `GET /healthz/ready` | none | yes (public) | — (intentionally public) |
| `POST /notify/email` | **none** | yes | Cloudflare Access service token only |
| `GET /dlq/failed` | **none** | yes | Cloudflare Access service token only |
| `POST /dlq/replay/{id}` | **none** | yes | Cloudflare Access service token only |
| `POST /admin/config/refresh` | **`ADMIN_TOKEN` bearer check** | **no** (not in ingress → 404) | `ADMIN_TOKEN` + private-network-only |

> **Correction vs. earlier docs:** `ADMIN_TOKEN` protects **only** `POST /admin/config/refresh`.
> The DLQ endpoints (`/dlq/failed`, `/dlq/replay/*`) do **not** check `ADMIN_TOKEN` — they
> are protected solely by Cloudflare Access at the network layer. `/dlq/replay/*` can
> re-send any failed email and `/dlq/failed` exposes recipient and subject metadata, so
> treat the Access policy on `/dlq/*` as security-critical.

Because `/notify/email` and `/dlq/*` have no app-layer auth, the perimeter relies entirely on **Cloudflare Tunnel + a correctly configured Cloudflare Access policy**. This is appropriate for a trusted single-operator home-server deployment where:

- The Cloudflare Access policy is configured **before** the tunnel is activated;
- The beacon-server port (`6969`) is not directly exposed to the internet — only the tunnel reaches it.

If the Access policy is misconfigured or bypassed, or beacon-server's port is reachable on an internal network without equivalent controls, any client can send arbitrary email and query/replay the DLQ.

**Admin endpoint.** `POST /admin/config/refresh` requires `Authorization: Bearer <ADMIN_TOKEN>`. When `ADMIN_TOKEN` is unset the endpoint is disabled (returns `403`); with it set, a wrong/absent bearer returns `401`. It is also not routed through the tunnel (Section 6), so it is only reachable on the private network. Do not leave `ADMIN_TOKEN` unset if you intend to use config refresh.

**Planned work.** Per-service API-key authentication at the application layer is fully designed in [`docs/future-scope.md`](./future-scope.md). Once implemented, each service would present `Authorization: Bearer bkn_svc_<key>` scoped to specific categories, so a compromised Cloudflare policy would no longer be sufficient on its own to abuse the relay.

---

## 9. Observability

### Temporal UI

| Path | Description |
|------|-------------|
| `http://<host>:8080` | Temporal UI — workflow list, history, task-queue pollers |

The Temporal UI is bound to `localhost:8080` by the Docker Compose port mapping. Do not expose port 8080 directly to the internet. Access it via SSH port-forwarding or an internal network only.

Use the UI to confirm a worker is actually polling its queue: open the `email-<name>-queue` task queue and check that pollers are present. A provider with no poller means workflows enqueue but never deliver.

### Health probes

**Only the server exposes HTTP health endpoints.** The worker is a Temporal worker process with no HTTP listener — observe it via the Temporal UI (poller presence) and process/journal status instead.

```bash
# Liveness — server process is up
curl http://<host>:6969/healthz/live
# Expected: HTTP 200, body: ok

# Readiness
curl http://<host>:6969/healthz/ready
# Expected: HTTP 200, body: ready
```

Readiness semantics: the server marks itself ready once it has started and **successfully loaded config** (it exits at startup if config cannot load, so a running server always has valid config). Readiness does **not** verify Temporal connectivity — the server starts even when Temporal is unreachable, in which case `/notify/email` and `/dlq/*` return `503 temporal service not available` until Temporal becomes reachable and the server is restarted.

The Docker Compose `beacon-server` healthcheck calls `/healthz/ready` every 15 seconds (5 s timeout, 5 retries, 10 s start period).

### Logs

**Docker Compose:**

```bash
# Follow server logs
docker compose -f deploy/docker-compose.yml logs -f beacon-server

# Follow a specific worker
docker compose -f deploy/docker-compose.yml logs -f beacon-worker-sendgrid

# Follow all services
docker compose -f deploy/docker-compose.yml logs -f
```

**Systemd:**

```bash
# Server
journalctl -u beacon-server -f

# Worker instance (e.g. sendgrid-transactional)
journalctl -u beacon-worker@sendgrid-transactional -f

# All beacon units
journalctl -u 'beacon-*' -f

# Cloudflare Tunnel
journalctl -u cloudflared -f
```

Both binaries log structured JSON to stdout (`slog`). The worker logs its resolved `provider` and `task_queue` at startup — a quick way to confirm `PROVIDER_NAME` resolved to the queue you expect.

---

## 10. Upgrade, Rollback, Backup, and Scaling

### Upgrade (Docker Compose)

Beacon uses the `beacon:local` image tag. To upgrade to a new version:

```bash
# Rebuild the image from the new source
docker build -t beacon:local .

# Restart the affected services to pick up the new image
docker compose -f deploy/docker-compose.yml up -d --no-deps beacon-server beacon-worker-sendgrid
```

For production use, prefer explicit version tags (e.g. `beacon:v1.2.0`) instead of `beacon:local` to make rollbacks deterministic:

```bash
docker build -t beacon:v1.2.0 .
# Update image: in docker-compose.yml, change `image: beacon:local` to `image: beacon:v1.2.0`
docker compose -f deploy/docker-compose.yml up -d --no-deps beacon-server beacon-worker-sendgrid
```

### Rollback

To roll back to a previous image tag, set the `image:` field back to the old tag and re-deploy:

```bash
# Edit docker-compose.yml to restore image: beacon:v1.1.0
docker compose -f deploy/docker-compose.yml up -d --no-deps beacon-server beacon-worker-sendgrid
```

**Systemd:** replace the binaries at `/usr/local/bin/server` and `/usr/local/bin/email_worker` with the previous version and restart:

```bash
systemctl restart beacon-server
systemctl restart beacon-worker@sendgrid-transactional
```

### PostgreSQL backup and restore

The Temporal PostgreSQL data lives in the `postgres-data` named Docker volume. Use `pg_dump` against the running container to take a logical backup:

```bash
# Create a backup
docker compose -f deploy/docker-compose.yml exec postgresql \
  pg_dump -U temporal temporal > temporal-backup-$(date +%Y%m%d%H%M%S).sql

# Restore from a backup (target cluster must be running and the temporal DB must exist)
docker compose -f deploy/docker-compose.yml exec -T postgresql \
  psql -U temporal temporal < temporal-backup-20260624120000.sql
```

For a full volume-level backup (offline, more reliable for large datasets):

```bash
# Stop the cluster first to ensure consistency
docker compose -f deploy/docker-compose.yml stop postgresql temporal temporal-ui

# Back up the named volume
docker run --rm \
  -v beacon_postgres-data:/data:ro \
  -v $(pwd):/backup \
  alpine \
  tar czf /backup/postgres-data-$(date +%Y%m%d%H%M%S).tar.gz -C /data .

# Restart after backup
docker compose -f deploy/docker-compose.yml start postgresql temporal temporal-ui
```

### Scaling workers

Each provider has its own task queue, `email-<name>-queue`. To handle higher throughput for a single provider, run multiple worker instances **with the same `PROVIDER_NAME`** — they all poll that provider's queue and Temporal distributes work across them.

**Docker Compose** — use `--scale` (the service must not have a fixed container name):

```bash
docker compose -f deploy/docker-compose.yml up -d --scale beacon-worker-sendgrid=3
```

**Systemd** — the template unit does not support scaling a single instance name. Start additional instances with distinct names and the same `PROVIDER_NAME` override:

```bash
# Create an override drop-in for a second instance serving the same provider
systemctl edit --force beacon-worker@sendgrid-transactional-2
# In the editor, add:
# [Service]
# Environment=PROVIDER_NAME=sendgrid-transactional

systemctl enable --now beacon-worker@sendgrid-transactional-2
```

To add a **new provider** (rather than scale an existing one), provision its SMTP credentials in Infisical under `/beacon/smtp` (Section 4) and start a worker with `PROVIDER_NAME` set to the new provider's `name`. No changes to beacon-server are required; it hot-reloads config from Infisical every `CONFIG_POLL_INTERVAL` seconds (or immediately via `POST /admin/config/refresh`).
