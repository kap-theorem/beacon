# Beacon Configuration — Infisical Integration

## Overview

Beacon uses Infisical to manage SMTP provider configurations at runtime. Configuration is loaded at startup with bounded retry (5 attempts over ~31s), with automatic fallback to the previous config on refresh failure.

## Environment Variables

Set these environment variables before starting any Beacon service:

```bash
# Infisical server address (required)
export INFISICAL_ADDR="https://infisical.example.com"

# Authentication (Machine Identity — recommended for production)
export INFISICAL_CLIENT_ID="your-machine-identity-client-id"
export INFISICAL_CLIENT_SECRET="your-machine-identity-client-secret"
# Alternative: set INFISICAL_API_KEY (API key) or INFISICAL_TOKEN (legacy token)

# Infisical project (required)
export INFISICAL_PROJECT_ID="your-project-id"
export INFISICAL_ENVIRONMENT="prod"

# Temporal configuration
export TEMPORAL_ADDRESS="localhost:7233"
export TEMPORAL_NAMESPACE="default"
```

See [Configuration Reference](../CONFIGURATION.md) for the full list of variables.

## Infisical Setup

### 1. Create Project Structure

In Infisical, create the following folder hierarchy under your workspace:

```
/beacon/
├── smtp/
│   ├── sendgrid
│   ├── aws-ses
│   └── mailgun
└── auth/
    └── keys
```

### 2. Add SMTP Provider Configs

For each provider, create a secret with the provider name as the key. The value should be a JSON object:

**Path**: `/beacon/smtp/sendgrid`

```json
{
  "name": "sendgrid",
  "provider": "sendgrid",
  "host": "smtp.sendgrid.net",
  "port": 587,
  "username": "apikey",
  "password": "SG.your-sendgrid-api-key",
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

**Path**: `/beacon/smtp/aws-ses`

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

### 3. Required Fields

All SMTP configurations must include:

| Field | Type | Example | Notes |
|-------|------|---------|-------|
| name | string | "sendgrid" | Unique identifier for the provider |
| provider | string | "sendgrid" | Provider name (display/reference) |
| host | string | "smtp.sendgrid.net" | SMTP server hostname |
| port | int | 587 | SMTP server port (1-65535) |
| username | string | "apikey" | SMTP username |
| password | string | "SG.xxx" | SMTP password (not logged) |
| auth_type | string | "PLAIN" or "LOGIN" or "OAUTH2" | Authentication type |
| tls.enabled | bool | true | Enable TLS encryption |
| tls.server_name | string | "smtp.sendgrid.net" | TLS server name (if TLS enabled) |

### 4. Optional Fields

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| timeout | string | "30s" | Connection timeout duration |
| max_retries | int | 3 | Retry attempts per send |
| max_per_hour | int | 0 | Rate limit (0 = unlimited) |
| rules.daily_limit | int | 0 | Daily send limit (0 = unlimited) |
| rules.hourly_limit | int | 0 | Hourly send limit (0 = unlimited) |

## Health Checks

Beacon exposes health check endpoints:

### Liveness Probe
```bash
curl http://localhost:6969/healthz/live
# Returns 200 while the service is running
```

### Readiness Probe
```bash
curl http://localhost:6969/healthz/ready
# Returns 503 until config is loaded
# Returns 200 once config is ready
```

## Logging

Configuration loading is logged as JSON with the following fields:

```json
{
  "timestamp": "2026-04-26T10:30:45Z",
  "level": "INFO",
  "message": "config loaded successfully",
  "component": "config",
  "providers": 3,
  "revision": 1,
  "duration_ms": 245,
  "infisical_retries": 0
}
```

Check logs for any validation errors or connection issues:

```bash
# Validation error example
{
  "timestamp": "2026-04-26T10:30:47Z",
  "level": "ERROR",
  "message": "config validation failed",
  "errors": [
    {"field": "sendgrid.host", "reason": "required"},
    {"field": "ses.port", "reason": "out of range"}
  ]
}
```

## Troubleshooting

### Config Load Fails at Startup

1. **Check Infisical Connection**:
   ```bash
   # Replace $INFISICAL_TOKEN with the credential for your auth method (see Environment Variables above)
   curl -H "Authorization: Bearer $INFISICAL_TOKEN" \
     "$INFISICAL_ADDR/api/v4/secrets?workspaceId=&environment=prod&secretPath=/beacon/smtp"
   ```

2. **Validate JSON Format**:
   Each secret value must be valid JSON. Test with:
   ```bash
   echo '{"host":"smtp.example.com","port":587}' | jq .
   ```

3. **Check Infisical Paths**:
   Ensure the paths `/beacon/smtp/*` exist in your Infisical workspace.

### Config Validation Fails

Check the error logs for specific field failures. Common issues:

- **"host: required"** — Ensure the `host` field is present
- **"port: out of range"** — Port must be 1-65535
- **"auth_type: invalid"** — Must be PLAIN, LOGIN, or OAUTH2
- **"tls.server_name: required when TLS is enabled"** — If `tls.enabled: true`, add `tls.server_name`

## Deployment via Cloudflare Tunnel

To deploy Beacon behind a Cloudflare Tunnel:

1. **Install Cloudflare Tunnel**:
   ```bash
   brew install cloudflared
   cloudflared login
   ```

2. **Create Tunnel Configuration**:
   ```yaml
   # ~/.cloudflared/config.yml
   tunnel: beacon-prod
   credentials-file: /path/to/credentials

   ingress:
     - hostname: beacon.example.com
       service: http://localhost:6969
     - service: http_status:404
   ```

3. **Start Tunnel**:
   ```bash
   cloudflared tunnel run beacon-prod
   ```

4. **Verify**:
   ```bash
   curl https://beacon.example.com/healthz/ready
   ```

## API Endpoints

See [docs/API.md](../API.md) for the complete API reference including the DLQ and admin endpoints.

### Send Email (quick reference)
```bash
POST /notify/email
Content-Type: application/json

{
  "to": "user@example.com",
  "subject": "Welcome!",
  "body": "Hello from Beacon",
  "client_hint": "payments"  # optional; routes to a matching SMTP provider
}
```

Response (202 Accepted):
```json
{
  "success": true,
  "message": "email notification triggered",
  "data": {
    "workflow_id": "email-workflow-...",
    "workflow_run_id": "...",
    "provider": "sendgrid"
  }
}
```
