# Infisical Integration Quick Start

## TL;DR — 5 Steps to Production

### 1️⃣ Create API Token in Infisical
```
Settings → API Tokens → Create API Token
Name: beacon-service
Scope: Read /beacon/* secrets
Copy: Your token value (e.g., k8qTW...)
```

### 2️⃣ Create Folder Structure
```
Infisical UI → Create Folders:
/beacon
/beacon/smtp
```

### 3️⃣ Add Your Email Provider Config
```
Path: /beacon/smtp/sendgrid
Key: sendgrid
Value: (paste JSON config with your credentials)
```

### 4️⃣ Set Environment Variables
```bash
export INFISICAL_ADDR="https://app.infisical.com"
export INFISICAL_PROJECT_ID="your-project-id"
export INFISICAL_ENVIRONMENT="prod"
# Dev/testing: use INFISICAL_TOKEN (legacy API token)
export INFISICAL_TOKEN="k8qTW..."
# Production: use Machine Identity instead — see "Environment Variables Explained" below
export TEMPORAL_ADDRESS="localhost:7233"
export TEMPORAL_NAMESPACE="default"
```

### 5️⃣ Start Beacon
```bash
go run cmd/server/main.go

# Check logs for: "config loaded successfully"
# Check health: curl http://localhost:6969/healthz/ready
```

---

## Detailed Flow

```
┌─────────────────────────────────────────────────────────┐
│                   Your Application                       │
│                  (Beacon Services)                       │
└──────────────────┬──────────────────────────────────────┘
                   │
                   ↓ (Environment Variables)
          ┌────────────────────┐
          │ INFISICAL_ADDR     │
          │ INFISICAL_TOKEN    │
          └────────────────────┘
                   │
                   ↓ (HTTP API Call)
          ┌────────────────────┐
          │   Infisical        │
          │   Server           │
          │   (Cloud or        │
          │    Self-Hosted)    │
          └────────────────────┘
                   │
                   ↓ (Returns JSON)
          ┌────────────────────────┐
          │  /beacon/smtp/         │
          │  ├── sendgrid          │
          │  ├── mailgun           │
          │  └── ses               │
          └────────────────────────┘
                   │
                   ↓ (Caches in Memory)
          ┌────────────────────┐
          │  ConfigService     │
          │  (Validates &      │
          │   Stores Config)   │
          └────────────────────┘
                   │
                   ↓ (On Request)
          ┌────────────────────┐
          │ Email Activities   │
          │ (Use Provider      │
          │  Config)           │
          └────────────────────┘
```

---

## Configuration File Structure in Infisical

```
Your Workspace
├── Environments
│   └── prod (default)
│       └── Secrets
│           └── /beacon/
│               ├── /smtp/
│               │   ├── sendgrid
│               │   │   Value: {...JSON config...}
│               │   ├── mailgun
│               │   │   Value: {...JSON config...}
│               │   └── ses
│               │       Value: {...JSON config...}
│               └── /auth/
│                   └── (future use for API keys)
```

---

## JSON Config Template

Replace values with your actual credentials:

```json
{
  "name": "sendgrid",
  "provider": "sendgrid",
  "host": "smtp.sendgrid.net",
  "port": 587,
  "username": "apikey",
  "password": "SG.YOUR-API-KEY-HERE",
  "auth_type": "PLAIN",
  "tls": {
    "enabled": true,
    "server_name": "smtp.sendgrid.net"
  },
  "timeout": "30s",
  "max_retries": 3,
  "max_per_hour": 0
}
```

### Key Field Definitions:
- **name** — Display name for this provider
- **provider** — Provider identifier (sendgrid, mailgun, ses, etc.)
- **host** — SMTP server hostname
- **port** — SMTP server port (587 for TLS, 465 for SSL)
- **username** — SMTP username (often "apikey" for API-based providers)
- **password** — SMTP password (store securely in Infisical)
- **auth_type** — Authentication method (PLAIN, LOGIN, OAUTH2)
- **tls** — TLS configuration (enabled + server name)
- **timeout** — Connection timeout (default 30s)
- **max_retries** — Retry attempts (default 3)
- **max_per_hour** — Rate limit (0 = unlimited)

---

## Environment Variables Explained

| Variable | Where to Get | Example |
|----------|-------------|---------|
| **INFISICAL_ADDR** | Infisical dashboard URL or your self-hosted URL | `https://app.infisical.com` |
| **INFISICAL_PROJECT_ID** | Infisical project settings | `abc123...` |
| **INFISICAL_ENVIRONMENT** | Environment name in your Infisical project | `prod` |
| **INFISICAL_TOKEN** | Settings → API Tokens → Create & Copy (dev/testing) | `k8qTW...` |
| **INFISICAL_CLIENT_ID** | Machine Identity → Create → Client ID (production) | `mi-abc...` |
| **INFISICAL_CLIENT_SECRET** | Machine Identity → Create → Client Secret (production) | `secret...` |
| **TEMPORAL_ADDRESS** | Your Temporal server | `localhost:7233` |
| **TEMPORAL_NAMESPACE** | Temporal namespace | `default` |

> **Production auth**: Use `INFISICAL_CLIENT_ID` + `INFISICAL_CLIENT_SECRET` (Machine Identity) for production deployments. `INFISICAL_TOKEN` is supported but is the legacy method. See [Configuration Reference](../CONFIGURATION.md#authentication) for the full auth priority order.

### Where to Set Them

**Option 1: Terminal (for development)**
```bash
export INFISICAL_ADDR="https://app.infisical.com"
export INFISICAL_TOKEN="k8qTW..."
go run cmd/server/main.go
```

**Option 2: .env file (for development)**
```bash
# Create file: .env.local
INFISICAL_ADDR=https://app.infisical.com
INFISICAL_TOKEN=k8qTW...
TEMPORAL_ADDRESS=localhost:7233
TEMPORAL_NAMESPACE=default

# Load and run
source .env.local
go run cmd/server/main.go
```

**Option 3: Docker (for deployment)**
```bash
docker run \
  -e INFISICAL_ADDR="https://app.infisical.com" \
  -e INFISICAL_TOKEN="k8qTW..." \
  -p 6969:6969 \
  beacon:latest
```

**Option 4: Kubernetes (for production)**

Use Machine Identity credentials for production (see [Configuration Reference](../CONFIGURATION.md#authentication)):

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: infisical-credentials
type: Opaque
stringData:
  addr: "https://app.infisical.com"
  project-id: "your-project-id"
  client-id: "your-machine-identity-client-id"
  client-secret: "your-machine-identity-client-secret"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: beacon-http
spec:
  template:
    spec:
      containers:
      - name: http-server
        env:
        - name: INFISICAL_ADDR
          valueFrom:
            secretKeyRef:
              name: infisical-credentials
              key: addr
        - name: INFISICAL_PROJECT_ID
          valueFrom:
            secretKeyRef:
              name: infisical-credentials
              key: project-id
        - name: INFISICAL_ENVIRONMENT
          value: "prod"
        - name: INFISICAL_CLIENT_ID
          valueFrom:
            secretKeyRef:
              name: infisical-credentials
              key: client-id
        - name: INFISICAL_CLIENT_SECRET
          valueFrom:
            secretKeyRef:
              name: infisical-credentials
              key: client-secret
```

---

## Testing Steps

### Test 1: Verify Infisical Connection
```bash
curl -X GET \
  -H "Authorization: Bearer $INFISICAL_TOKEN" \
  "$INFISICAL_ADDR/api/v4/secrets?environment=prod&secretPath=/beacon/smtp"

# Expected: 200 OK with JSON array of your secrets
```

### Test 2: Start Beacon and Check Logs
```bash
go run cmd/server/main.go

# Look for these log lines:
# "config loaded successfully"
# "providers": N (number of providers)
# "revision": 1
```

### Test 3: Health Checks
```bash
# Terminal 2
curl http://localhost:6969/healthz/live
# Expected: 200 OK, body: "ok"

curl http://localhost:6969/healthz/ready
# Expected: 200 OK, body: "ready"
```

### Test 4: Send Email (requires Temporal)
```bash
curl -X POST http://localhost:6969/notify/email \
  -H "Content-Type: application/json" \
  -d '{
    "to": "test@example.com",
    "subject": "Test Email",
    "body": "Hello from Beacon with real Infisical!"
  }'

# Expected: 202 Accepted (if Temporal is running)
# Or: 503 Service Unavailable (if Temporal not running - that's OK for testing config)
```

---

## Troubleshooting Quick Reference

| Issue | Check | Fix |
|-------|-------|-----|
| Connection refused | INFISICAL_ADDR is correct | Use https://app.infisical.com for cloud |
| Invalid token (401) | Token exists in Infisical UI | Regenerate token, ensure no extra spaces |
| Path not found (404) | Folder structure `/beacon/smtp` exists | Create folders in Infisical UI |
| Validation errors | JSON syntax in secret value | Validate with `jq`: `echo '{}' \| jq .` |
| providers: 0 | Check environment is `prod` | Verify folder contains secrets with values |
| Config loads but empty | Secret keys might be wrong | Ensure secret keys match provider names |

---

## Success Indicators

When everything is working:

✅ Infisical server is accessible
✅ API token works and has read permissions
✅ Folder structure exists in Infisical
✅ Provider configs added as secrets
✅ Server logs: "config loaded successfully"
✅ Server logs: "providers: N"
✅ Health endpoint returns 200 OK
✅ No validation errors in logs

---

## What to Do Next

Once your real Infisical integration works:

### Short Term
1. **Test with multiple providers** — Add SendGrid, Mailgun, and AWS SES to Infisical
2. **Test email sending** — Use `client_hint` to route emails to different providers
3. **Monitor logs** — Watch for validation errors or connection issues

### Medium Term
1. **Enable hot-reload** — Set `CONFIG_POLL_INTERVAL` to auto-refresh configs from Infisical
2. **Set up alerting** — Monitor cache staleness (24h threshold)
3. **Implement secret rotation** — Plan for API key rotation strategy

### Long Term
1. **Multi-region deployment** — Deploy Beacon to multiple regions with same Infisical
2. **Cloudflare tunnel** — Use your INFISICAL_ADDR and INFISICAL_TOKEN in tunnel config
3. **Production hardening** — Add monitoring, logging aggregation, alerting

---

## Files Reference

| File | Purpose |
|------|---------|
| `INFISICAL_SETUP.md` | Detailed step-by-step guide |
| `CONFIG.md` | Configuration documentation |
| `QUICK_TEST.md` | Mock server testing |
| `internal/config/` | Config service code |

---

## Support

For issues:
1. Check `INFISICAL_SETUP.md` troubleshooting section
2. Verify environment variables are set correctly
3. Test Infisical connection directly with curl
4. Check server logs for specific error messages
5. Review the checklist to ensure all steps completed
