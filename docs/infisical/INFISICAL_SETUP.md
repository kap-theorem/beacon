# Setting Up Beacon with Real Infisical Server

## Step 1: Access Your Infisical Instance

First, determine how you'll access Infisical:

### Option A: Infisical Cloud (Hosted)
- Go to https://app.infisical.com
- Sign up or log in
- Create a new organization/project if needed

### Option B: Self-Hosted Infisical
- If you have a self-hosted instance, access it at your server URL (e.g., https://infisical.mycompany.com)
- Ensure it's accessible from your development machine

### Option C: Local Docker (Quick Setup)
```bash
# Start Infisical locally in Docker
docker run -d \
  --name infisical \
  -p 8000:8000 \
  -e ENCRYPTION_KEY=$(openssl rand -hex 16) \
  infisical/infisical:latest
  
# Access at http://localhost:8000
```

---

## Step 2: Create a Service Token

In Infisical UI, create an API token for your Beacon service:

### In Infisical Dashboard:
1. Go to **Settings** → **API Tokens** (or **Machine Identities** in newer versions)
2. Click **Create API Token**
3. Give it a name: `beacon-service`
4. Select **Scopes**: 
   - Read secrets in `/beacon/*` paths
5. Copy the token value (you'll need this for `INFISICAL_TOKEN`)

**Token Format**: Usually starts with `k8qTW...` or similar

---

## Step 3: Create Folder Structure in Infisical

In your Infisical project:

### Create Folders:
1. Create folder: `/beacon`
2. Inside `/beacon`, create: `/smtp`
3. Inside `/beacon`, create: `/auth`

**UI Steps**:
- Click **Secrets** in the project
- Click **+ New Secret** → Select **Folder**
- Name it `beacon`
- Repeat for `/beacon/smtp` and `/beacon/auth`

---

## Step 4: Add SMTP Provider Configs

For each email provider, create a secret in `/beacon/smtp/` with the provider name.

### Example: SendGrid

**Path**: `/beacon/smtp/sendgrid`

**Key**: `config` (or any key name)

**Value** (paste entire JSON):
```json
{
  "name": "sendgrid",
  "provider": "sendgrid",
  "host": "smtp.sendgrid.net",
  "port": 587,
  "username": "apikey",
  "password": "SG.your-actual-sendgrid-api-key",
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

### Example: AWS SES

**Path**: `/beacon/smtp/ses`

**Value**:
```json
{
  "name": "ses",
  "provider": "aws-ses",
  "host": "email-smtp.us-east-1.amazonaws.com",
  "port": 587,
  "username": "your-smtp-username",
  "password": "your-smtp-password",
  "auth_type": "LOGIN",
  "tls": {
    "enabled": true,
    "server_name": "email-smtp.us-east-1.amazonaws.com"
  },
  "timeout": "30s",
  "max_retries": 3,
  "max_per_hour": 0
}
```

### Example: Mailgun

**Path**: `/beacon/smtp/mailgun`

**Value**:
```json
{
  "name": "mailgun",
  "provider": "mailgun",
  "host": "smtp.mailgun.org",
  "port": 587,
  "username": "postmaster@your-domain.mailgun.org",
  "password": "your-mailgun-smtp-password",
  "auth_type": "PLAIN",
  "tls": {
    "enabled": true,
    "server_name": "smtp.mailgun.org"
  },
  "timeout": "30s",
  "max_retries": 3,
  "max_per_hour": 0
}
```

### UI Steps to Add Secrets:
1. Navigate to `/beacon/smtp` folder
2. Click **+ Add Secret**
3. **Key**: `sendgrid` (or provider name)
4. **Value**: Paste the JSON config above
5. Click **Save**
6. Repeat for each provider

---

## Step 5: Get Your Infisical Details

You need:
1. **INFISICAL_ADDR** — Your Infisical server URL
2. **INFISICAL_TOKEN** — The API token you created

### Find INFISICAL_ADDR:

- **Cloud**: `https://app.infisical.com`
- **Self-hosted**: Your server URL (e.g., `https://infisical.mycompany.com`)
- **Docker local**: `http://localhost:8000`

### Verify Access:

Test that your token works:
```bash
curl -X GET \
  -H "Authorization: Bearer YOUR_INFISICAL_TOKEN" \
  "YOUR_INFISICAL_ADDR/api/v4/secrets?workspaceId=YOUR_WORKSPACE_ID&environment=prod&secretPath=/beacon/smtp"
```

Should return a 200 response with your secrets.

---

## Step 6: Update Environment Variables

Set these before running Beacon services:

```bash
# Your Infisical server details
export INFISICAL_ADDR="https://app.infisical.com"  # or your server URL
export INFISICAL_TOKEN="k8qTW..."  # Your API token

# Existing Temporal config
export TEMPORAL_ADDRESS="localhost:7233"
export TEMPORAL_NAMESPACE="default"
```

### Option A: Set in Terminal
```bash
export INFISICAL_ADDR="https://app.infisical.com"
export INFISICAL_TOKEN="your-token-here"

# Run HTTP server
go run cmd/server/main.go
```

### Option B: Create .env.local File
```bash
# .env.local
INFISICAL_ADDR=https://app.infisical.com
INFISICAL_TOKEN=your-token-here
TEMPORAL_ADDRESS=localhost:7233
TEMPORAL_NAMESPACE=default
```

Then source it:
```bash
source .env.local
go run cmd/server/main.go
```

### Option C: Docker/K8s (for deployment)
Add to your deployment manifest:
```yaml
env:
  - name: INFISICAL_ADDR
    value: "https://app.infisical.com"
  - name: INFISICAL_TOKEN
    valueFrom:
      secretKeyRef:
        name: infisical-credentials
        key: token
```

---

## Step 7: Test the Integration

### Test 1: Check Health Endpoints

```bash
# Liveness (should always return 200)
curl http://localhost:6969/healthz/live

# Readiness (should return 200 after config loads)
curl http://localhost:6969/healthz/ready
```

### Test 2: Check Server Logs

When the service starts, you should see:
```json
{
  "level": "INFO",
  "message": "config loaded successfully",
  "providers": 3,
  "revision": 1
}
```

### Test 3: Verify Providers Loaded

```bash
# This requires a debug endpoint (not exposed yet, but you can add one)
# For now, check the startup logs show "providers: 3" (or however many you added)
```

### Test 4: Debug Connection Issues

If config fails to load, check:

```bash
# 1. Verify token works
curl -X GET \
  -H "Authorization: Bearer $INFISICAL_TOKEN" \
  "$INFISICAL_ADDR/api/v4/secrets?environment=prod&secretPath=/beacon/smtp"

# 2. Check logs for error messages
# Look for: "validation error", "connection error", "auth failure"

# 3. Verify folder structure
# Ensure /beacon/smtp folder exists in Infisical
```

---

## Troubleshooting

### ❌ "Connection refused" or timeout
**Solution**: 
- Verify INFISICAL_ADDR is correct and accessible
- Check if Infisical is running: `curl $INFISICAL_ADDR`
- Check firewall/network rules

### ❌ "Invalid token" (401 error)
**Solution**:
- Verify INFISICAL_TOKEN is correct
- Check token hasn't expired in Infisical UI
- Ensure token has correct scopes (read on `/beacon/*`)

### ❌ "Path not found" (404 error)
**Solution**:
- Verify folder structure exists: `/beacon/smtp/`
- Check provider names match exactly (case-sensitive)
- Ensure secrets are in the correct environment (e.g., `prod`)

### ❌ "Validation errors: [field required]"
**Solution**:
- Check JSON syntax in the secret value
- Verify all required fields present (name, provider, host, port, username, auth_type)
- Use `jq` to validate: `echo '{"field":"value"}' | jq .`

### ❌ "providers: 0" (no providers loaded)
**Solution**:
- Verify secrets exist at `/beacon/smtp/*` paths
- Check Infisical token has read access to those paths
- Verify environment is set correctly (default is `prod`)

---

## Working Configuration Example

If you get to this point, your setup is complete:

```
HTTP Server Startup Logs:
✓ Config loaded successfully
✓ Providers: 3 (sendgrid, mailgun, ses)
✓ Revision: 1
✓ HTTP server starting on :6969

Health Checks:
✓ /healthz/live → 200 OK
✓ /healthz/ready → 200 OK

Infisical Folder Structure:
✓ /beacon/smtp/sendgrid (JSON config)
✓ /beacon/smtp/mailgun (JSON config)
✓ /beacon/smtp/ses (JSON config)
```

---

## Next Steps

Once your real Infisical integration is working:

1. **Test email sending** — Use `client_hint` to route emails to different providers
2. **Add more providers** — Update Infisical, no code changes needed
3. **Enable hot-reload** — Set `CONFIG_POLL_INTERVAL` to auto-refresh configs from Infisical
4. **Monitor in production** — Check health endpoints, review logs
5. **Deploy to Cloudflare** — Use the INFISICAL_ADDR and INFISICAL_TOKEN in your deployment

---

## Reference: All Required Fields

| Field | Type | Required | Example |
|-------|------|----------|---------|
| name | string | Yes | "sendgrid" |
| provider | string | Yes | "sendgrid" |
| host | string | Yes | "smtp.sendgrid.net" |
| port | number | Yes | 587 |
| username | string | Yes | "apikey" |
| password | string | Yes | "SG.xxxx" |
| auth_type | string | Yes | "PLAIN" or "LOGIN" |
| tls.enabled | boolean | Yes | true |
| tls.server_name | string | Yes (if TLS enabled) | "smtp.sendgrid.net" |
| timeout | string | No | "30s" (default) |
| max_retries | number | No | 3 (default) |
| max_per_hour | number | No | 0 = unlimited |
