# Machine Identity Setup - Step by Step with Screenshots

## Overview

You'll create a Machine Identity in Infisical that Beacon can use to authenticate and read secrets.

---

## Step 1: Navigate to Machine Identities

### In your Infisical dashboard:

1. Click on **Organization** dropdown (top left, where it says "kaplabs")
2. Click on **Settings** or **Access Control**
3. Look for **Machine Identities** tab (or similar)

**You should see:**
- List of existing machine identities (if any)
- A button to create a new one (+ Create, New Identity, etc.)

---

## Step 2: Create Machine Identity

### Click "Create Machine Identity" or "+ New"

Fill in:
- **Name**: `beacon-service`
- **Description**: `Beacon notification service - reads email provider configs`
- Other fields (optional): Leave as default

Click **Create** or **Save**

---

## Step 3: Get Client Credentials

After creating the Machine Identity, you'll see a credentials section.

### You need ONE of:

**Option A: Client Secret (Most Common)**
```
┌─────────────────────────────────────┐
│ beacon-service                      │
├─────────────────────────────────────┤
│ Credentials:                        │
│                                     │
│ Client Secret:                      │
│ [+ Create Client Secret]            │
└─────────────────────────────────────┘
```

Click **"Create Client Secret"** or **"+ Add Client Secret"**

You'll get:
```
Client ID:     0da652d1-1234-5678-...
Client Secret: YXNkZmhqYXNkZmhq...
```

**⚠️ Copy these immediately and save them!** You can only see them once.

---

**Option B: API Key**
```
Click "Create API Key" or "+ Add API Key"

You'll get:
API Key: k8qTW2m5nL9pQ...
```

---

## Step 4: Set Permissions for the Machine Identity

### Go to your Beacon project:

1. Click **beacon** project (or whichever project has your secrets)
2. Go to **Settings** or **Access Control**
3. Look for **Members**, **Identities**, or **Access** section
4. Find your `beacon-service` Machine Identity
5. Set permissions:

```
┌────────────────────────────────────────┐
│ beacon-service                         │
├────────────────────────────────────────┤
│ Environment:    prod                   │
│ Role/Access:    Read (or Can Read)     │
│ Path Filter:    /beacon/* (optional)   │
└────────────────────────────────────────┘
```

Click **Save** or **Apply**

---

## Step 5: Verify Folder Structure in Infisical

Make sure you have:

```
beacon (Project)
└── prod (Environment)
    └── Secrets
        └── /beacon/
            ├── /smtp/
            │   ├── sendgrid
            │   ├── mailgun
            │   └── ses
            └── /auth/
```

Each provider should have a secret with JSON config:

```
Path: /beacon/smtp/sendgrid
Value: {"name":"sendgrid","provider":"sendgrid",...}
```

---

## Step 6: Set Environment Variables

Now that you have your credentials, set them:

### Using Client Secret:

```bash
export INFISICAL_ADDR="https://app.infisical.com"
export INFISICAL_CLIENT_ID="0da652d1-1234-5678-..."
export INFISICAL_CLIENT_SECRET="YXNkZmhqYXNkZmhq..."
export TEMPORAL_ADDRESS="localhost:7233"
export TEMPORAL_NAMESPACE="default"
```

### Or using API Key:

```bash
export INFISICAL_ADDR="https://app.infisical.com"
export INFISICAL_API_KEY="k8qTW2m5nL9pQ..."
export TEMPORAL_ADDRESS="localhost:7233"
export TEMPORAL_NAMESPACE="default"
```

### Verify they're set:

```bash
echo "Client ID: $INFISICAL_CLIENT_ID"
echo "Client Secret: $INFISICAL_CLIENT_SECRET"
# Or
echo "API Key: $INFISICAL_API_KEY"
```

Both should show values (not empty)

---

## Step 7: Test the Connection

### Test 1: Direct curl to Infisical

```bash
# With Client ID (Machine Identity):
curl -X GET \
  -H "Authorization: Bearer $INFISICAL_CLIENT_ID" \
  "$INFISICAL_ADDR/api/v4/secrets?environment=prod&secretPath=/beacon/smtp"

# Or with API Key:
curl -X GET \
  -H "Authorization: Bearer $INFISICAL_API_KEY" \
  "$INFISICAL_ADDR/api/v4/secrets?environment=prod&secretPath=/beacon/smtp"

# Expected: 200 OK with your secrets as JSON
```

### Test 2: Start Beacon

```bash
go run cmd/server/main.go

# Expected logs:
# "using machine identity authentication"
# "config loaded successfully"
# "providers": N (number of providers loaded)
# "auth_method": "client-secret"
```

### Test 3: Health Checks

```bash
# In another terminal:
curl http://localhost:6969/healthz/live
# Expected: 200 OK

curl http://localhost:6969/healthz/ready
# Expected: 200 OK
```

---

## Success Indicators

✅ You should see:

```
HTTP Server Logs:
- "using machine identity authentication"
- "config loaded successfully"
- "providers": 3 (or however many you added)
- "auth_method": "client-secret"

Health Checks:
- /healthz/live → 200 OK
- /healthz/ready → 200 OK

Direct Infisical curl:
- Returns 200 OK
- Response includes your provider configs as JSON
```

---

## Troubleshooting

### Problem: "Unauthorized" or "Invalid credentials"

**Solution:**
1. Check Client ID and Client Secret are correct
2. Verify Machine Identity was created successfully
3. Test credentials with curl command above

### Problem: "Forbidden" or "Access Denied"

**Solution:**
1. Go to beacon project → Access Control
2. Verify `beacon-service` Machine Identity is listed
3. Check it has "Read" permission on `prod` environment
4. Click Save to apply permissions

### Problem: "Not Found" - Path `/beacon/smtp` doesn't exist

**Solution:**
1. Go to beacon project → Secrets
2. Create folder: `/beacon`
3. Inside that, create folder: `/beacon/smtp`
4. Add your provider configs there

### Problem: Beacon logs show "providers: 0"

**Solution:**
1. Verify secrets exist in `/beacon/smtp/`
2. Check secret values are valid JSON
3. Verify secret names match what code expects

### Problem: curl returns empty array

**Solution:**
1. Check secrets are in correct environment (prod)
2. Verify secrets have values (not just keys)
3. Check path is correct: `/beacon/smtp`

---

## Supported Authentication Methods

Beacon now supports (in order of preference):

1. **Machine Identity (Client ID + Secret)** ← Recommended
   ```bash
   export INFISICAL_CLIENT_ID="0da652d1-..."
   export INFISICAL_CLIENT_SECRET="YXNkZmhq..."
   ```

2. **API Key**
   ```bash
   export INFISICAL_API_KEY="k8qTW2m5nL9pQ..."
   ```

3. **Legacy Token** (for backward compatibility)
   ```bash
   export INFISICAL_TOKEN="old-token-format"
   ```

Beacon auto-detects which credentials are provided and uses them.

---

## Quick Checklist

- [ ] Machine Identity `beacon-service` created
- [ ] Client Secret generated (have Client ID + Secret)
- [ ] Permissions set on beacon project
- [ ] Folder `/beacon/smtp/` exists
- [ ] At least one provider config added
- [ ] Environment variables set:
  - [ ] INFISICAL_ADDR
  - [ ] INFISICAL_CLIENT_ID
  - [ ] INFISICAL_CLIENT_SECRET
- [ ] curl test returns 200 OK
- [ ] Beacon starts with "config loaded successfully"
- [ ] Health checks return 200 OK

---

## Environment Variables Cheat Sheet

```bash
# Machine Identity Method (Recommended)
export INFISICAL_ADDR="https://app.infisical.com"
export INFISICAL_CLIENT_ID="0da652d1-1234-5678-abcd-ef1234567890"
export INFISICAL_CLIENT_SECRET="YXNkZmhqYXNkZmhqYXNkZmhqYXNkZmhq"

# OR API Key Method
export INFISICAL_ADDR="https://app.infisical.com"
export INFISICAL_API_KEY="k8qTW2m5nL9pQrStUvWxYz1aB2cD3eF4"

# Temporal (existing)
export TEMPORAL_ADDRESS="localhost:7233"
export TEMPORAL_NAMESPACE="default"

# Start Beacon
go run cmd/server/main.go
```

---

## Next Steps

Once Machine Identity is working:

1. ✅ Config loads from Infisical
2. ⬜ Test with multiple email providers
3. ⬜ Integrate with U2 (multi-provider routing)
4. ⬜ Enable U3 (hot-reload on config changes)
5. ⬜ Deploy to Cloudflare tunnel

Enjoy secure configuration management! 🔐
