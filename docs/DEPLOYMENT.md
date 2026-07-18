# Beacon — Deployment Runbook

**Target environment**: Self-hosted home server  
**Stack**: Self-hosted Temporal cluster (Postgres-backed) + per-provider Beacon workers + Beacon server + Cloudflare Tunnel

---

## Table of Contents

1. [Overview and Topology](#1-overview-and-topology)
2. [Prerequisites](#2-prerequisites)
3. [Temporal Cluster Bring-Up](#3-temporal-cluster-bring-up)
4. [Infisical Setup](#4-infisical-setup)
5. [Beacon Server and Per-Provider Workers](#5-beacon-server-and-per-provider-workers)
6. [Cloudflare Tunnel](#6-cloudflare-tunnel)
7. [Security Note](#7-security-note)
8. [Observability](#8-observability)
9. [Upgrade, Rollback, Backup, and Scaling](#9-upgrade-rollback-backup-and-scaling)

---

## 1. Overview and Topology

Beacon is an async email notification service. Upstream services submit notification requests over HTTPS through a Cloudflare Tunnel; the HTTP server enqueues Temporal workflows; per-provider workers execute SMTP delivery.

### Topology

```
Downstream services (auth-service, app-backend, …)
         |
         | HTTPS  beacon.example.com
         v
+---------------------------+
|   Cloudflare Tunnel       |
|   (cloudflared)           |
|   /healthz/*  — public    |
|   /notify/email  \        |
|   /dlq/*          > Access protected (service tokens)
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
         | Temporal task queue  email-task-queue
         v
+---------------------------+   +---------------------------+
|  beacon-worker-sendgrid   |   |  beacon-worker-mailgun    |  (one per provider)
|  cmd/email_worker         |   |  cmd/email_worker         |
|  PROVIDER_NAME=sendgrid   |   |  PROVIDER_NAME=mailgun    |
+---------------------------+   +---------------------------+
         |                               |
         v                               v
   smtp.sendgrid.net              smtp.mailgun.org
```

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

## 4. Infisical Setup

Beacon reads SMTP provider configuration from Infisical at the path `/beacon/smtp`. Each secret under that path is one SMTP provider config.

### Secret structure

Each secret has:
- **Key**: the provider name (e.g. `sendgrid-transactional`, `mailgun-marketing`)
- **Value**: a JSON object matching the structure shown in `infisical-example.json`

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
  "is_default": true
}
```

### Rules

- Exactly one provider must have `"is_default": true`. Beacon uses this provider when no `client_hint` is given in an email request.
- The `categories` array drives routing: when `POST /notify/email` includes a `client_hint`, Beacon resolves the provider whose `categories` list contains that hint. `categories: []` means the provider is never selected by hint.
- The `provider` field (e.g. `"sendgrid"`, `"mailgun"`) must match the `PROVIDER_NAME` set on the corresponding worker instance.

### Create the secrets in Infisical

1. Open your Infisical project and navigate to **Secrets** for the target environment (e.g. `prod`).
2. Create a secret at path `/beacon/smtp` for each provider, using the provider name as the key and the JSON object above as the value.
3. Repeat for each additional provider (e.g. `mailgun-marketing` with `"is_default": false`).

See `infisical-example.json` in the repository root for the full structure of both a default and a non-default provider.

### Create a machine identity

1. In Infisical, go to **Access Control → Machine Identities** and create a new identity (e.g. `beacon-prod`).
2. Under the identity, create a **Universal Auth** client secret and record:
   - `CLIENT_ID` → set as `INFISICAL_CLIENT_ID`
   - `CLIENT_SECRET` → set as `INFISICAL_CLIENT_SECRET`
3. Grant the identity read access to your project for the target environment.
4. Note your project's **Project ID** from the project settings page.

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

Do not set `PROVIDER_NAME` in `worker.env`; it is injected per-service via the `environment:` block in `docker-compose.yml`.

Start beacon-server and the sendgrid worker:

```bash
docker compose -f deploy/docker-compose.yml up -d beacon-server beacon-worker-sendgrid
```

#### Adding additional providers (Compose)

Copy the `beacon-worker-sendgrid` service block in `deploy/docker-compose.yml` and change the service name and `PROVIDER_NAME` for each new provider:

```yaml
beacon-worker-mailgun:
  image: beacon:local
  command: ["/usr/local/bin/email_worker"]
  env_file:
    - ./env/worker.env
  environment:
    PROVIDER_NAME: mailgun
    TEMPORAL_ADDRESS: temporal:7233
  depends_on:
    - temporal
  restart: unless-stopped
```

Then bring up the new worker:

```bash
docker compose -f deploy/docker-compose.yml up -d beacon-worker-mailgun
```

Each worker instance polls the same Temporal task queue (`email-task-queue`) and routes delivery through the SMTP config loaded from Infisical for the provider name set in `PROVIDER_NAME`.

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

# Enable and start one worker per provider (template unit — %i expands to instance name)
systemctl enable --now beacon-worker@sendgrid
systemctl enable --now beacon-worker@mailgun
```

The `beacon-worker@.service` template unit sets `Environment=PROVIDER_NAME=%i`, so `beacon-worker@sendgrid` automatically sets `PROVIDER_NAME=sendgrid` and `beacon-worker@mailgun` sets `PROVIDER_NAME=mailgun`. The shared `/etc/beacon/worker.env` is loaded for all instances.

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

For the Docker Compose path, place the credentials file and updated config under `deploy/cloudflared/` (this directory is bind-mounted into the `cloudflared` container as `/etc/cloudflared`):

```bash
docker compose -f deploy/docker-compose.yml up -d cloudflared
```

For the systemd path, copy both files to `/etc/cloudflared/` before starting the unit.

### Create a Cloudflare Access application

This step is mandatory before exposing `/notify/email` or `/dlq/*` publicly. Without it, `/notify/email` is an open relay reachable by any client that can reach the tunnel hostname.

1. In the Cloudflare Zero Trust dashboard, go to **Access → Applications → Add an application**.
2. Select **Self-Hosted**.
3. Set the application domain to `beacon.example.com` and the path to `/notify/email`. Repeat for `/dlq/*` (or create one application covering both paths via a wildcard if your plan supports it).
4. Under **Policies**, add a policy requiring a **Service Token**.
5. Go to **Access → Service Auth → Service Tokens** and create a token for each downstream service that needs to call Beacon. Record the `CF-Access-Client-Id` and `CF-Access-Client-Secret` headers; downstream services must include both headers on every request.

The ingress rule in `deploy/cloudflared/config.yml` routes `/healthz/*` to beacon-server without any Access policy — health probes from infrastructure tooling do not require authentication. All other paths either require an Access policy (via the application you just created) or return 404 (catch-all).

---

## 7. Security Note

Beacon has no application-layer authentication as of this release. The `/notify/email` endpoint accepts any well-formed request that reaches the server, making it an open relay at the application layer.

The only protection currently in place is network-layer enforcement via Cloudflare Access service tokens (described in Section 6). This is appropriate for a trusted single-operator home-server deployment where:

- The Cloudflare Access policy is correctly configured before the tunnel is activated
- No other path bypasses the tunnel (i.e. the beacon-server port is not directly exposed to the internet)

If the Cloudflare Access policy is misconfigured, momentarily bypassed, or the beacon-server port is reachable on an internal network without equivalent controls, any client can send arbitrary email through Beacon.

Future work to add per-service API-key authentication at the application layer is fully designed and documented in `docs/future-scope.md`. That design would make a compromised Cloudflare policy insufficient on its own to abuse the relay.

**Admin and DLQ endpoints** (`/admin/config/refresh`, `/dlq/failed`, `/dlq/replay/*`) are additionally protected by the `ADMIN_TOKEN` bearer check in `server.env`. Do not leave `ADMIN_TOKEN` unset; an unset token disables these endpoints entirely (returns 403).

---

## 8. Observability

### Temporal UI

| Path | Description |
|------|-------------|
| `http://<host>:8080` | Temporal UI — workflow list, history, task queue status |

The Temporal UI is bound to `localhost:8080` by the Docker Compose port mapping. Do not expose port 8080 directly to the internet. Access it via SSH port-forwarding or an internal network only.

### Health probes

Both the server and worker expose health endpoints:

```bash
# Liveness — server process is up
curl http://<host>:6969/healthz/live
# Expected: HTTP 200, body: ok

# Readiness — server connected to Temporal and config loaded
curl http://<host>:6969/healthz/ready
# Expected: HTTP 200, body: ready
```

The Docker Compose `beacon-server` healthcheck calls `/healthz/ready` every 15 seconds.

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

# Worker instance (e.g. sendgrid)
journalctl -u beacon-worker@sendgrid -f

# All beacon units
journalctl -u 'beacon-*' -f

# Cloudflare Tunnel
journalctl -u cloudflared -f
```

---

## 9. Upgrade, Rollback, Backup, and Scaling

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
systemctl restart beacon-worker@sendgrid
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

Each provider needs exactly one worker instance per task-queue poller. To handle higher throughput for a single provider, run multiple instances of the same worker — all instances of `beacon-worker-sendgrid` (or `beacon-worker@sendgrid`) poll the same `email-task-queue` and Temporal distributes work across them.

**Docker Compose** — use `--scale` (the service must not have a fixed container name):

```bash
docker compose -f deploy/docker-compose.yml up -d --scale beacon-worker-sendgrid=3
```

**Systemd** — the template unit does not support scaling a single instance name. Start additional instances with distinct names and a shared `PROVIDER_NAME` override:

```bash
# beacon-worker@sendgrid-2.service — create an override drop-in
systemctl edit --force beacon-worker@sendgrid-2
# In the editor, add:
# [Service]
# Environment=PROVIDER_NAME=sendgrid

systemctl enable --now beacon-worker@sendgrid-2
```

To add a new provider in either deployment model, provision its SMTP credentials in Infisical under `/beacon/smtp` (following Section 4) and start a new worker instance with `PROVIDER_NAME` set to the new provider name. No changes to the beacon-server are required; config is hot-reloaded from Infisical every `CONFIG_POLL_INTERVAL` seconds.
