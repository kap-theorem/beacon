# Infisical Machine Identity Integration for Beacon

## Quick Start (Machine Identity)

### 1️⃣ Create Machine Identity in Infisical
```
Organization → Settings/Access Control → Machine Identities
+ Create Machine Identity
Name: beacon-service
```

### 2️⃣ Get Credentials
```
Click on beacon-service → Create Client Secret
Copy: Client ID and Client Secret
```

### 3️⃣ Set Permissions
```
beacon project → Access Control
Add beacon-service Machine Identity
Set: Read permission on /beacon/* path
```

### 4️⃣ Create Secret Structure
```
/beacon/
├── /smtp/
│   ├── sendgrid (JSON config)
│   ├── mailgun (JSON config)
│   └── ses (JSON config)
└── /auth/ (for future use)
```

### 5️⃣ Set Environment Variables
```bash
export INFISICAL_ADDR="https://app.infisical.com"
export INFISICAL_CLIENT_ID="0da652d1-..."
export INFISICAL_CLIENT_SECRET="YXNkZmhq..."
export TEMPORAL_ADDRESS="localhost:7233"
export TEMPORAL_NAMESPACE="default"
```

### 6️⃣ Start Beacon
```bash
go run cmd/server/main.go

# Expected output:
# "using machine identity authentication"
# "config loaded successfully"
# "providers": N
```

### 7️⃣ Test
```bash
curl http://localhost:6969/healthz/ready
# Expected: 200 OK
```

---

## Authentication Methods Supported

Beacon supports multiple authentication methods for Infisical:

### 1. Machine Identity (Recommended) ⭐
```bash
export INFISICAL_CLIENT_ID="0da652d1-..."
export INFISICAL_CLIENT_SECRET="YXNkZmhq..."
```
✅ Most secure
✅ Audit logging
✅ Fine-grained permissions
✅ Recommended for production

### 2. API Key
```bash
export INFISICAL_API_KEY="k8qTW2m5nL9pQ..."
```
✅ Simpler setup
✅ Single credential
⚠️ Less granular control

### 3. Legacy Token (Backward Compatible)
```bash
export INFISICAL_TOKEN="old-token-format"
```
✅ Works with old setup
⚠️ Legacy method

---

## Environment Variables Reference

| Variable | Required? | Where to Get | Example |
|----------|-----------|-------------|---------|
| INFISICAL_ADDR | Yes | Your Infisical instance | https://app.infisical.com |
| INFISICAL_CLIENT_ID | If Machine Identity | Machine Identity → Client Secret | 0da652d1-... |
| INFISICAL_CLIENT_SECRET | If Machine Identity | Machine Identity → Client Secret | YXNkZmhq... |
| INFISICAL_API_KEY | If API Key | Machine Identity → API Key | k8qTW2m... |
| INFISICAL_TOKEN | Legacy | Old setup | (deprecated) |
| TEMPORAL_ADDRESS | Yes | Your Temporal server | localhost:7233 |
| TEMPORAL_NAMESPACE | Yes | Temporal config | default |

---

## How Beacon Detects Auth Method

Beacon automatically detects which credentials to use in this order:

1. **Machine Identity** (if INFISICAL_CLIENT_ID + INFISICAL_CLIENT_SECRET set)
2. **API Key** (if INFISICAL_API_KEY set)
3. **Legacy Token** (if INFISICAL_TOKEN set)
4. **None** (use empty auth - will fail if Infisical requires auth)

---

## Detailed Guides

| Document | Purpose | Read Time |
|----------|---------|-----------|
| **MACHINE_IDENTITY_STEPS.md** | Step-by-step with examples | 10 min |
| **MACHINE_IDENTITY_SETUP.md** | Complete reference guide | 15 min |
| **CONFIG.md** | Config structure & deployment | 10 min |
| **QUICK_TEST.md** | Testing with mock server | 5 min |

---

## Testing Your Setup

### Test 1: Verify Credentials
```bash
echo "Client ID: $INFISICAL_CLIENT_ID"
echo "Secret: $INFISICAL_CLIENT_SECRET"
```

### Test 2: Direct Infisical Connection
```bash
curl -X GET \
  -H "Authorization: Bearer $INFISICAL_CLIENT_ID" \
  "$INFISICAL_ADDR/api/v4/secrets?environment=prod&secretPath=/beacon/smtp"

# Expected: 200 OK with your secrets
```

### Test 3: Beacon Startup
```bash
go run cmd/server/main.go

# Expected logs:
# - "using machine identity authentication"
# - "config loaded successfully"
# - "providers": N
```

### Test 4: Health Endpoints
```bash
curl http://localhost:6969/healthz/live    # Always 200
curl http://localhost:6969/healthz/ready   # 200 after config loads
```

---

## Troubleshooting

### Unauthorized (401)
- Verify Client ID and Client Secret are correct
- Check Machine Identity exists in Infisical
- Ensure credentials haven't expired

### Forbidden (403)
- Check Machine Identity has permissions on beacon project
- Go to project → Access Control and set read permission
- Verify path filter includes `/beacon/*`

### Not Found (404)
- Verify folder `/beacon/smtp/` exists in Infisical
- Check secret names match provider names
- Confirm secrets are in correct environment (prod)

### Validation Errors
- Validate JSON in secrets: `echo '{}' | jq .`
- Check all required fields present (host, port, username, etc.)
- Verify field types (port is number, not string)

### Providers: 0
- Secrets exist but not loading
- Check environment is `prod`
- Verify credentials have read access to paths
- Test with curl first

---

## JSON Config Format

Each provider secret should be valid JSON:

```json
{
  "name": "sendgrid",
  "provider": "sendgrid",
  "host": "smtp.sendgrid.net",
  "port": 587,
  "username": "apikey",
  "password": "SG.your-actual-key",
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

**Required Fields:**
- name, provider, host, port, username, auth_type, tls (enabled & server_name)

**Optional Fields:**
- password, timeout, max_retries, max_per_hour

---

## Security Best Practices

✅ **DO:**
- Use Machine Identity (not plain tokens)
- Store Client Secret in secret manager (not in code)
- Rotate credentials regularly
- Use path filters to limit access
- Enable audit logging
- Use HTTPS for Infisical communication

❌ **DON'T:**
- Commit credentials to git
- Use same credentials across environments
- Store secrets in .env file in repo
- Disable TLS for Infisical connection
- Use overly broad permissions

---

## Deployment Options

### Local Development
```bash
export INFISICAL_ADDR="https://app.infisical.com"
export INFISICAL_CLIENT_ID="..."
export INFISICAL_CLIENT_SECRET="..."
go run cmd/server/main.go
```

### Docker
```bash
docker run \
  -e INFISICAL_ADDR="https://app.infisical.com" \
  -e INFISICAL_CLIENT_ID="..." \
  -e INFISICAL_CLIENT_SECRET="..." \
  -p 6969:6969 \
  beacon:latest
```

### Kubernetes
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: infisical-credentials
stringData:
  client-id: "0da652d1-..."
  client-secret: "YXNkZmhq..."
---
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - name: beacon
        env:
        - name: INFISICAL_ADDR
          value: "https://app.infisical.com"
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

### Cloudflare Tunnel
```bash
# Set env vars in your deployment
export INFISICAL_ADDR="https://app.infisical.com"
export INFISICAL_CLIENT_ID="..."
export INFISICAL_CLIENT_SECRET="..."

# Deploy as usual
go run cmd/server/main.go

# Tunnel configuration (cloudflared config.yml)
ingress:
  - hostname: beacon.yourdomain.com
    service: http://localhost:6969
```

---

## Monitoring

Watch for these logs:

✅ **Success:**
```json
{
  "level": "INFO",
  "message": "config loaded successfully",
  "providers": 3,
  "revision": 1,
  "auth_method": "client-secret"
}
```

⚠️ **Warning (recoverable):**
```json
{
  "level": "WARN",
  "message": "infisical unreachable, retrying",
  "attempt": 1,
  "backoff": "1s"
}
```

❌ **Error (blocking):**
```json
{
  "level": "ERROR",
  "message": "config validation failed",
  "errors": [{"field": "host", "reason": "required"}]
}
```

---

## Next Steps

1. ✅ Set up Machine Identity in Infisical
2. ✅ Configure Beacon with credentials
3. ⬜ Test connection with health endpoints
4. ⬜ Implement U2 (multi-provider email routing)
5. ⬜ Enable U3 (dynamic config hot-reload)
6. ⬜ Deploy to production via Cloudflare Tunnel

---

## Support Resources

- **MACHINE_IDENTITY_STEPS.md** — Visual step-by-step guide
- **MACHINE_IDENTITY_SETUP.md** — Complete reference
- **CONFIG.md** — Configuration details
- **INFISICAL_QUICKSTART.md** — Quick reference (legacy API token version)

---

For more information about Infisical Machine Identities, see:
- https://infisical.com/docs/machine-identities

For Beacon documentation:
- See CONFIG.md, TESTING.md, and other guides in this directory
