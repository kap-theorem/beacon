# Infisical Machine Identity Setup

## What is a Machine Identity?

A **Machine Identity** is Infisical's secure way for applications/services to authenticate. It replaces the older API token approach with:
- Better security (short-lived credentials)
- Audit logging
- Granular access control
- Workload identity support

---

## Step 1: Create a Machine Identity

### In Infisical Dashboard:

1. Go to your **Organization** → **kaplabs**
2. Navigate to **Access Control** or **Settings**
3. Look for **"Machine Identities"** tab
4. Click **"+ Create Machine Identity"** or **"Create Identity"**
5. Fill in:
   - **Name**: `beacon-service`
   - **Description**: `Beacon notification service config reader`
6. Click **Create**

---

## Step 2: Get Your Machine Identity Credentials

After creating the Machine Identity, you'll see credentials:

You need to create an **API Key** or **Client Secret** for this Machine Identity.

### Create a Client Secret:

1. Click on your `beacon-service` Machine Identity
2. Look for **"Client Secrets"** or **"Credentials"** section
3. Click **"+ Create Client Secret"** or **"Add Secret"**
4. You'll get:
   - **Client ID** (looks like: `0da652d1-...`)
   - **Client Secret** (looks like: `xxxxxx...`)
5. **Copy both values immediately** (you can only see them once)

### If you see "API Key" instead:
1. Click **"Create API Key"**
2. Copy the **API Key** value

---

## Step 3: Set Access Permissions

Grant the Machine Identity access to read your secrets:

1. Go to **Project Settings** (beacon project)
2. Look for **"Access Control"** or **"Members/Identities"** tab
3. Find your `beacon-service` Machine Identity in the list
4. Set permissions:
   - **Environment**: `prod` (or your environment)
   - **Access Level**: `Read` (or `Can Read Secrets`)
   - **Path Filter**: `/beacon/*` (optional but recommended)
5. Click **Save**

---

## Step 4: Configure Beacon to Use Machine Identity

The Beacon config service needs to use your Machine Identity credentials instead of an API token.

### Update Environment Variables:

```bash
# Instead of INFISICAL_TOKEN, use:
export INFISICAL_ADDR="https://app.infisical.com"
export INFISICAL_CLIENT_ID="0da652d1-..."
export INFISICAL_CLIENT_SECRET="xxxxxx..."

# Or if using API Key:
export INFISICAL_ADDR="https://app.infisical.com"
export INFISICAL_API_KEY="your-api-key-here"

# Existing Temporal config
export TEMPORAL_ADDRESS="localhost:7233"
export TEMPORAL_NAMESPACE="default"
```

### Update Beacon Code (service.go)

We need to update the config service to support Machine Identity authentication. Let me create the updated version:

---

## Step 5: Update Beacon Configuration Service

Replace your current `internal/config/service.go` with the Machine Identity version below.

This will handle both:
- Old API Token method (for backward compatibility)
- New Machine Identity method (recommended)

The service will auto-detect which credentials are provided and use the appropriate auth method.

---

## Testing Your Machine Identity Setup

### Test 1: Verify Machine Identity Credentials

```bash
# Check if credentials are set
echo "CLIENT_ID: $INFISICAL_CLIENT_ID"
echo "CLIENT_SECRET: $INFISICAL_CLIENT_SECRET"

# Both should have values (not empty)
```

### Test 2: Test Infisical Connection

```bash
# With Machine Identity (Client ID + Secret):
curl -X GET \
  -H "Authorization: Bearer $INFISICAL_CLIENT_ID" \
  "$INFISICAL_ADDR/api/v4/secrets?environment=prod&secretPath=/beacon/smtp"

# Or with API Key:
curl -X GET \
  -H "Authorization: Bearer $INFISICAL_API_KEY" \
  "$INFISICAL_ADDR/api/v4/secrets?environment=prod&secretPath=/beacon/smtp"

# Expected: 200 OK with your secrets in JSON
```

### Test 3: Start Beacon with Machine Identity

```bash
export INFISICAL_ADDR="https://app.infisical.com"
export INFISICAL_CLIENT_ID="0da652d1-..."
export INFISICAL_CLIENT_SECRET="xxxxxx..."

go run cmd/server/main.go

# Expected logs:
# "config loaded successfully"
# "providers": N
```

### Test 4: Health Checks

```bash
curl http://localhost:6969/healthz/live
curl http://localhost:6969/healthz/ready
```

---

## Environment Variable Reference

| Variable | Type | Required | Where to Find |
|----------|------|----------|---------------|
| **INFISICAL_ADDR** | URL | Yes | Your Infisical instance (https://app.infisical.com or self-hosted) |
| **INFISICAL_CLIENT_ID** | String | If using Client Secret | Machine Identity → Client Secret → Client ID |
| **INFISICAL_CLIENT_SECRET** | String | If using Client Secret | Machine Identity → Client Secret → Client Secret |
| **INFISICAL_API_KEY** | String | If using API Key | Machine Identity → API Key |

---

## Comparison: Old vs New

| Feature | Old (API Token) | New (Machine Identity) |
|---------|-----------------|----------------------|
| Auth Method | Single token | Client ID + Secret |
| Security | Basic | Better (short-lived) |
| Audit Log | Limited | Full audit trail |
| Credential Rotation | Manual | Can be automated |
| Scope Control | Project-level | Fine-grained (path-level) |

---

## Troubleshooting Machine Identity

### Issue: "Invalid credentials" or 401 error
```bash
# Check credentials are set
echo $INFISICAL_CLIENT_ID
echo $INFISICAL_CLIENT_SECRET

# Verify Machine Identity exists in Infisical UI
# Check permissions are set for the project
```

### Issue: "Access Denied" or 403 error
```bash
# Machine Identity created, but permissions not set
# Go to Project → Access Control
# Add your Machine Identity with "Read" permission
# Set path filter to /beacon/* if available
```

### Issue: "Not Found" or 404 error
```bash
# Path /beacon/smtp doesn't exist
# Create folder structure in Infisical:
# /beacon → /beacon/smtp
# Add secrets with provider configs
```

---

## File Format: Secrets in Infisical

In Infisical, your secrets should look like:

**Path**: `/beacon/smtp/sendgrid`

**Format** (Key-Value):
- **Key**: `config` (or any name)
- **Value**: Your JSON config

```json
{
  "name": "sendgrid",
  "provider": "sendgrid",
  "host": "smtp.sendgrid.net",
  "port": 587,
  "username": "apikey",
  "password": "SG.your-actual-api-key",
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

---

## Next Steps

1. ✅ Create Machine Identity `beacon-service` in Infisical
2. ✅ Create Client Secret (get Client ID + Secret)
3. ✅ Set permissions on your `beacon` project
4. ✅ Create folder structure (`/beacon/smtp/`) and add provider configs
5. ⬜ Update Beacon code to support Machine Identity (next step)
6. ⬜ Set environment variables
7. ⬜ Test connection and start Beacon

---

## Quick Checklist

- [ ] Machine Identity `beacon-service` created
- [ ] Client Secret generated (have Client ID + Secret)
- [ ] Permissions set on `beacon` project for the Machine Identity
- [ ] Folder structure exists: `/beacon/smtp/`
- [ ] At least one provider config added (e.g., sendgrid)
- [ ] Environment variables ready to be set
- [ ] Beacon code updated (awaiting update)

