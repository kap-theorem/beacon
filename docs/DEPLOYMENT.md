# Beacon — Deployment Runbook

**Target environment**: Self-hosted home server
**Stack**: Self-hosted Temporal cluster (Postgres-backed) + per-channel/provider Beacon workers + Beacon server + Cloudflare Tunnel

---

## Table of Contents

1. [Overview and Topology](#1-overview-and-topology)
2. [Prerequisites](#2-prerequisites)
3. [Temporal Cluster Bring-Up](#3-temporal-cluster-bring-up)
4. [Infisical Setup (Control Plane)](#4-infisical-setup-control-plane)
5. [Beacon Server and Per-Provider Workers](#5-beacon-server-and-per-provider-workers)
6. [Cutover to the v1 API Surface](#6-cutover-to-the-v1-api-surface)
7. [Cloudflare Tunnel](#7-cloudflare-tunnel)
8. [Security Model](#8-security-model)
9. [Observability](#9-observability)
10. [Upgrade, Rollback, Backup, and Scaling](#10-upgrade-rollback-backup-and-scaling)

---

## 1. Overview and Topology

Beacon is an async notification service. Upstream services submit authenticated notification
requests over HTTPS through a Cloudflare Tunnel; the HTTP server enqueues Temporal workflows;
per-channel/provider workers execute delivery.

### Topology

```
Downstream services (auth-service, app-backend, …)
         |
         | HTTPS  beacon.example.com
         | Authorization: Bearer bk_...
         v
+---------------------------+
|   Cloudflare Tunnel       |
|   (cloudflared)           |
|   /healthz/*  — public    |
|   /v1/*          \        |
|                    > Access-protected (service tokens);
|                      app-layer API key still required
+---------------------------+
         |
         | http://beacon-server:6969  (internal)
         v
+---------------------------+
|   Beacon HTTP Server      |
|   cmd/server              |
|   port 6969               |
+---------------------------+
         |
         | gRPC  :7233
         v
+---------------------------+         +---------------------------+
|   Temporal Server         |         |   PostgreSQL :5432        |
|   temporalio/auto-setup   |<------->|   (temporal persistence)  |
|   :7233                   |         +---------------------------+
+---------------------------+
         |
         | Temporal task queues  {channel}-{provider}-queue
         v
+---------------------------+   +---------------------------+
| beacon-worker@email-      |   | beacon-worker@email-      |  (one per channel+provider)
|   sendgrid                |   |   mailgun                 |
| cmd/email_worker          |   | cmd/email_worker          |
| WORKER_SPEC=email-sendgrid|   | WORKER_SPEC=email-mailgun |
+---------------------------+   +---------------------------+
         |                               |
         v                               v
   smtp.sendgrid.net              smtp.mailgun.org
```

### Deployment Options

Two deployment models are supported and can be chosen based on your host environment:

- **Docker Compose** (`deploy/docker-compose.yml`): runs Temporal, PostgreSQL, Temporal UI,
  beacon-server, and each `beacon-worker-<provider>` as containers on a single Docker host. Use
  for straightforward single-node deployments.
- **Systemd units** (`deploy/systemd/`): runs `beacon-server` and per-instance
  `beacon-worker@<channel>-<provider>` as native system services. Use when Temporal runs
  separately (e.g. as its own Compose stack) and you want OS-level service management for Beacon
  binaries.

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

The multi-stage Dockerfile compiles both `server` and `email_worker` binaries from `cmd/server` and `cmd/email_worker` and copies them into a minimal `gcr.io/distroless/static-debian12:nonroot` image.

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

The `temporal` service depends on `postgresql` being healthy (pg_isready passes) before starting. The first boot may take 30–60 seconds while `auto-setup` runs schema migrations.

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

## 4. Infisical Setup (Control Plane)

Beacon reads its control plane from three Infisical secret paths (see `docs/CONFIGURATION.md` for
the full field reference):

| Secret path | Contents |
|---|---|
| `/beacon/providers/email` | One secret per SMTP provider |
| `/beacon/tenants` | One secret per tenant (a team/product owning services) |
| `/beacon/services` | One secret per registered calling service, including hashed API keys and per-channel policy |

> **Renamed from v1**: `/beacon/providers/email` was previously `/beacon/smtp`. If you are
> migrating an existing Infisical project, move the provider secrets to the new path — Beacon does
> not read the old one.

### Provision an SMTP provider

Create a secret at `/beacon/providers/email`, using the provider name as the key:

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
  "is_default": true
}
```

- Exactly one provider per channel should have `"is_default": true` — workers started without an
  explicit provider resolve to it, as does any service whose `channels.email.default_provider`
  points at it.
- `auth_type` must be `PLAIN` or `LOGIN`; `OAUTH2` is rejected at validation.
- Provider selection is no longer category-based (the old `categories` field is gone) — it is
  driven entirely by each service's `channels.email.providers` allowlist, set in `/beacon/services`
  below.

Repeat for each additional provider (e.g. `mailgun-marketing` with `"is_default": false`).

### Provision a tenant

Create a secret at `/beacon/tenants`, using the tenant id as the key:

```json
{ "tenant": "payments", "name": "Payments Team" }
```

### Provision a calling service (and its API key)

Every downstream caller needs a `/beacon/services/<service>` secret with at least one active API
key. Generate the key and its hash together:

```bash
KEY_SECRET=$(openssl rand -hex 24)
FULL_KEY="bk_k1_${KEY_SECRET}"
echo "Full key (hand to the calling service, only shown once): ${FULL_KEY}"
echo "sha256 (store in keys[].sha256 below): $(printf '%s' "${FULL_KEY}" | sha256sum | cut -d' ' -f1)"
```

Then create the secret at `/beacon/services`, key `billing-api`:

```json
{
  "service": "billing-api",
  "tenant": "payments",
  "enabled": true,
  "keys": [
    { "id": "k1", "sha256": "<sha256-hex-from-above>", "state": "active" }
  ],
  "channels": {
    "email": {
      "providers": ["sendgrid-transactional"],
      "default_provider": "sendgrid-transactional",
      "from": { "address": "billing@example.com", "name": "Billing" },
      "rate": { "rpm": 60, "daily": 5000 }
    }
  }
}
```

See `docs/CONFIGURATION.md` for the key-rotation procedure (adding a second active key before
removing the first) and the full field reference for all three secret paths.

### Create a machine identity

1. In Infisical, go to **Access Control → Machine Identities** and create a new identity (e.g. `beacon-prod`).
2. Under the identity, create a **Universal Auth** client secret and record:
   - `CLIENT_ID` → set as `INFISICAL_CLIENT_ID`
   - `CLIENT_SECRET` → set as `INFISICAL_CLIENT_SECRET`
3. Grant the identity read access to your project for the target environment.
4. Note your project's **Project ID** from the project settings page.

### Verify Infisical credentials

If Beacon fails to load config at startup, verify the machine identity works outside Beacon:

```bash
# 1. Exchange the machine identity for an access token
curl -s -X POST "$INFISICAL_ADDR/api/v1/auth/universal-auth/login" \
  -H "Content-Type: application/json" \
  -d '{"clientId": "'$INFISICAL_CLIENT_ID'", "clientSecret": "'$INFISICAL_CLIENT_SECRET'"}'
# Expect a JSON response containing "accessToken"

# 2. Fetch the provider secrets with that token
curl -s -H "Authorization: Bearer <accessToken-from-step-1>" \
  "$INFISICAL_ADDR/api/v4/secrets?projectId=$INFISICAL_PROJECT_ID&environment=prod&secretPath=/beacon/providers/email"
# Expect a JSON response containing a "secrets" array with one entry per provider
```

If step 1 fails, re-check the client ID/secret; if step 2 fails, re-check the identity's project access, the environment slug, and the secret path (repeat for `/beacon/tenants` and `/beacon/services`).

---

## 5. Beacon Server and Per-Provider Workers

### Docker Compose path

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

Do not set `WORKER_SPEC` in the shared `worker.env`; it is injected per-service via the `environment:` block in `docker-compose.yml`.

Start beacon-server and the sendgrid worker:

```bash
docker compose -f deploy/docker-compose.yml up -d beacon-server beacon-worker-sendgrid
```

#### Adding additional providers (Compose)

Copy the `beacon-worker-sendgrid` service block in `deploy/docker-compose.yml` and change the service name and `WORKER_SPEC` for each new provider:

```yaml
beacon-worker-mailgun:
  image: beacon:local
  command: ["/usr/local/bin/email_worker"]
  env_file:
    - ./env/worker.env
  environment:
    WORKER_SPEC: email-mailgun
    TEMPORAL_ADDRESS: temporal:7233
  depends_on:
    - temporal
  restart: unless-stopped
```

Then bring up the new worker:

```bash
docker compose -f deploy/docker-compose.yml up -d beacon-worker-mailgun
```

Each worker instance polls its `(channel, provider)`-specific Temporal task queue
(`email-<provider>-queue`, derived from `WORKER_SPEC`) and routes delivery through the SMTP config
loaded from Infisical for that provider name.

### Systemd path

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

# Enable and start one worker per channel+provider (template unit — %i expands to the instance name)
systemctl enable --now beacon-worker@email-sendgrid
systemctl enable --now beacon-worker@email-mailgun
```

The `beacon-worker@.service` template unit sets `Environment=WORKER_SPEC=%i`, so
`beacon-worker@email-sendgrid` automatically sets `WORKER_SPEC=email-sendgrid` and
`beacon-worker@email-mailgun` sets `WORKER_SPEC=email-mailgun`. `WORKER_SPEC` is parsed as
`<channel>-<provider>` — the channel is the segment before the first dash, the provider is
everything after (so provider names may themselves contain dashes). The shared
`/etc/beacon/worker.env` is loaded for all instances.

Both units run as the `beacon` system user with `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, and `PrivateTmp` hardening.

#### Install and enable the cloudflared unit

```bash
cp deploy/systemd/cloudflared.service /etc/systemd/system/

systemctl daemon-reload
systemctl enable --now cloudflared
```

The cloudflared unit runs as the `cloudflared` user. Create that user and ensure the credentials file and config are readable by it:

```bash
useradd --system --no-create-home cloudflared
chown -R cloudflared:cloudflared /etc/cloudflared
```

---

## 6. Cutover to the v1 API Surface

If you are upgrading from a pre-v2 Beacon deployment (unauthenticated `/notify/email`,
`client_hint`-based routing), cut over in this order so there is no window where workers or the
server are running against a control-plane shape they don't understand:

1. **Populate Infisical first** — create the `/beacon/providers/email` (renamed from
   `/beacon/smtp`), `/beacon/tenants`, and `/beacon/services` secrets for every provider, tenant,
   and calling service (Section 4). Do this before touching any running process.
2. **Deploy the workers** — roll out the new `email_worker` binary with `WORKER_SPEC=<channel>-<provider>`
   (or `CHANNEL`/`PROVIDER_NAME`) set per instance, so task queues matching the new provider config
   exist before traffic flows.
3. **Deploy the server** — roll out the new `server` binary, or (if the binary is already
   deployed) call `POST /admin/config/refresh` to force it to load the new bundle without a
   restart.
4. **Switch callers over**, all at once per caller (there is no dual-stack transition period —
   the old unauthenticated `/notify/email` route no longer exists):
   - New path: `POST /v1/notify/{channel}` (`{channel}` = `email`) instead of `/notify/email`.
   - Add an auth header to every request: `Authorization: Bearer bk_<keyid>_<secret>` or
     `X-API-Key: bk_<keyid>_<secret>`.
   - Drop `client_hint` from the request body — it no longer exists. Provider selection is via the
     new optional `provider` field (must be in the service's allowlist) or the service's configured
     default.
   - Optionally add an `Idempotency-Key` header for safe retries (see `docs/API.md`).
   - DLQ endpoints move to `GET /v1/dlq/failed` and `POST /v1/dlq/replay/{workflowID}`, both now
     requiring the same auth header (non-admin callers are scoped to their own tenant).
5. **Update the edge** — point the Cloudflare Access application and `cloudflared` ingress rules
   at the new `/v1/*` paths (Section 7).

Because rate-limit counters are in-memory (Section 8), a rolling restart during cutover resets
every service's rpm/daily counters to zero — expected, not a correctness bug; don't rely on
counters surviving a deploy.

---

## 7. Cloudflare Tunnel

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

Edit `deploy/cloudflared/config.yml` and replace both occurrences of `<TUNNEL_ID>` with your actual tunnel ID. The file should route `/v1/*` (not the old bare `/notify/email`) to the server:

```yaml
tunnel: abc123def456...
credentials-file: /etc/cloudflared/abc123def456....json

ingress:
  - hostname: beacon.example.com
    path: /healthz/*
    service: http://beacon-server:6969

  - hostname: beacon.example.com
    path: /v1/*
    service: http://beacon-server:6969

  - service: http_status:404
```

For the Docker Compose path, place the credentials file and updated config under `deploy/cloudflared/` (this directory is bind-mounted into the `cloudflared` container as `/etc/cloudflared`):

```bash
docker compose -f deploy/docker-compose.yml up -d cloudflared
```

For the systemd path, copy both files to `/etc/cloudflared/` before starting the unit.

### Create a Cloudflare Access application

Even though `/v1/*` now requires a per-service API key at the application layer (Section 8), a
Cloudflare Access application in front of it is still recommended as defense-in-depth — it stops
unauthenticated traffic at the edge, before it can even attempt (and fail) the app-layer key
check.

1. In the Cloudflare Zero Trust dashboard, go to **Access → Applications → Add an application**.
2. Select **Self-Hosted**.
3. Set the application domain to `beacon.example.com` and the path to `/v1/*`.
4. Under **Policies**, add a policy requiring a **Service Token**.
5. Go to **Access → Service Auth → Service Tokens** and create a token for each downstream service that needs to call Beacon. Record the `CF-Access-Client-Id` and `CF-Access-Client-Secret` headers; downstream services must include both headers **in addition to** their Beacon API key on every request.

The ingress rule in `deploy/cloudflared/config.yml` routes `/healthz/*` to beacon-server without any Access policy — health probes from infrastructure tooling do not require authentication. `/v1/*` requires both the Access policy above and a valid Beacon API key; the catch-all returns 404 for anything else.

---

## 8. Security Model

Every `POST /v1/notify/{channel}`, `GET /v1/dlq/failed`, and `POST /v1/dlq/replay/{workflowID}`
request requires a per-service API key, presented as `Authorization: Bearer bk_<keyid>_<secret>`
or `X-API-Key: bk_<keyid>_<secret>`. Beacon resolves it by SHA-256 hash lookup against the active
key(s) registered for a service in `/beacon/services` — plaintext keys are never stored or
compared. Unknown or invalid keys return 401; a registered-but-disabled service returns 403.

Per-service policy further restricts what an authenticated key may do:

- **Channel binding** — the channel must be present in the service's `channels` map, or the
  request is rejected (403).
- **Provider binding** — an explicit `provider` in the request must be in the service's allowlist,
  or the request is rejected (403); omitting it uses the service's configured default.
- **Sender lock** — the `From` address/name is always injected from the service's policy, never
  accepted from the request.
- **Rate limits** — a per-service, per-channel token bucket (rpm) and daily UTC quota. Both are
  **in-memory and reset on every process restart** — they bound abuse from a single key between
  deploys, not a durable, cross-restart audit trail.

Cloudflare Access (Section 7) is defense-in-depth layered in front of this app-layer check — it
blocks unauthenticated traffic at the edge, but the API-key check above is what actually
authorizes which service is calling and what it may do. Losing the Access policy (misconfiguration
or bypass) no longer means an open relay: an attacker reaching the server directly would still need
a valid, registered API key.

**Admin endpoint**: `POST /admin/config/refresh` uses its own `ADMIN_TOKEN` bearer check,
independent of the per-service key system above (an unset token disables that endpoint — returns
403). The same `ADMIN_TOKEN` also authenticates as an unscoped admin identity on the two `/v1/dlq/*`
endpoints (cross-tenant visibility for operators), but is explicitly rejected with 403 on
`/v1/notify/{channel}` — it is an operator credential, not a sending identity.

**Key rotation**: add a second active key (`k2`) alongside the existing one, deploy, migrate the
calling service to the new key, then remove the old one — see `docs/CONFIGURATION.md`.

---

## 9. Observability

### Temporal UI

| Path | Description |
|------|-------------|
| `http://<host>:8080` | Temporal UI — workflow list, history, task queue status |

The Temporal UI is bound to `localhost:8080` by the Docker Compose port mapping. Do not expose port 8080 directly to the internet. Access it via SSH port-forwarding or an internal network only.

### Health probes

Only the HTTP server exposes health endpoints — the email worker does not:

```bash
# Liveness — server process is up
curl http://<host>:6969/healthz/live
# Expected: HTTP 200, body: ok

# Readiness — config loaded AND Temporal reachable (5 s result cache)
curl http://<host>:6969/healthz/ready
# Expected: HTTP 200, body: ready
```

`/healthz/ready` returns `503` with body `not ready: <reason>` when either check fails. Wire your
container/service supervisor's health check to `/healthz/ready` if you want restarts gated on
Temporal reachability, not just process liveness.

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

# Worker instance (e.g. email-sendgrid)
journalctl -u beacon-worker@email-sendgrid -f

# All beacon units
journalctl -u 'beacon-*' -f

# Cloudflare Tunnel
journalctl -u cloudflared -f
```

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

For production use, prefer explicit version tags (e.g. `beacon:v2.0.0`) instead of `beacon:local` to make rollbacks deterministic:

```bash
docker build -t beacon:v2.0.0 .
# Update image: in docker-compose.yml, change `image: beacon:local` to `image: beacon:v2.0.0`
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
systemctl restart beacon-worker@email-sendgrid
```

> Rolling back past the v1-surface cutover also means reverting the control-plane secrets and
> caller changes from Section 6 — the pre-v2 binaries do not understand `/beacon/providers/email`,
> `/beacon/tenants`, or `/beacon/services`.

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

Each `(channel, provider)` pair needs at least one worker instance polling its task queue. To
handle higher throughput for a single provider, run multiple instances — all instances of
`beacon-worker-sendgrid` (or `beacon-worker@email-sendgrid`) poll the same queue
(`email-sendgrid-queue`) and Temporal distributes work across them.

**Docker Compose** — use `--scale` (the service must not have a fixed container name):

```bash
docker compose -f deploy/docker-compose.yml up -d --scale beacon-worker-sendgrid=3
```

**Systemd** — the template unit does not support scaling a single instance name. Start additional instances with distinct names and a shared `WORKER_SPEC` override:

```bash
# beacon-worker@email-sendgrid-2.service — create an override drop-in
systemctl edit --force beacon-worker@email-sendgrid-2
# In the editor, add:
# [Service]
# Environment=WORKER_SPEC=email-sendgrid

systemctl enable --now beacon-worker@email-sendgrid-2
```

To add a new provider in either deployment model: provision its SMTP credentials in Infisical
under `/beacon/providers/email` (Section 4), add it to the relevant services' `channels.email.providers`
allowlists, and start a new worker instance with `WORKER_SPEC` set to `<channel>-<new-provider>`.
No changes to the beacon-server are required; config is hot-reloaded from Infisical every
`CONFIG_POLL_INTERVAL` seconds (or immediately via `POST /admin/config/refresh`).
