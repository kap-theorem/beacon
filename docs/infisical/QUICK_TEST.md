# Quick Testing Guide — U1: Infisical Config Integration

## Fastest Way to Test (5 minutes)

```bash
# Run the automated test script (from the project root)
bash scripts/test-local.sh
```

This will:
1. ✓ Build mock Infisical server
2. ✓ Build HTTP server
3. ✓ Start mock Infisical on :8000 (with 3 test email providers)
4. ✓ Start HTTP server on :6969
5. ✓ Test liveness probe (/healthz/live)
6. ✓ Test readiness probe (/healthz/ready)
7. ✓ Test email notification endpoint (/notify/email)
8. ✓ Auto-cleanup on Ctrl+C

## Step-by-Step Manual Testing

### Step 1: Start Mock Infisical (Terminal 1)

```bash
go run scripts/mock-infisical.go -port 8000
```

Output:
```
Mock Infisical server listening on :8000
Config: fail-count=0, slow-ms=0, bad-json=false
```

### Step 2: Start HTTP Server (Terminal 2)

```bash
export INFISICAL_ADDR="http://localhost:8000"
# Any token value works with the mock server; for real Infisical use Machine Identity or API Key
export INFISICAL_TOKEN="test-token"
export TEMPORAL_ADDRESS="localhost:7233"
export TEMPORAL_NAMESPACE="default"

go run cmd/server/main.go
```

Expected output:
```json
{
  "timestamp": "2026-04-26T...",
  "level": "INFO",
  "message": "config service initialized",
  "providers": 3,
  "revision": 1
}
{
  "level": "INFO",
  "message": "HTTP server starting",
  "addr": ":6969"
}
```

### Step 3: Run Tests (Terminal 3)

```bash
# Test 1: Liveness (should always return 200)
curl -v http://localhost:6969/healthz/live
# Expected: HTTP/1.1 200 OK, body: "ok"

# Test 2: Readiness (should return 200 after config loads)
curl -v http://localhost:6969/healthz/ready
# Expected: HTTP/1.1 200 OK, body: "ready"

# Test 3: Send email (requires Temporal, but endpoint should respond)
curl -X POST http://localhost:6969/notify/email \
  -H "Content-Type: application/json" \
  -d '{
    "to": "test@example.com",
    "subject": "Hello",
    "body": "Test message"
  }'
# Expected: HTTP/1.1 202 Accepted (if Temporal is running)
```

## Test Retry Logic

Test that the service retries on transient errors:

```bash
# In Terminal 1, restart with 2 failures
go run scripts/mock-infisical.go -port 8000 -fail-count 2
```

In HTTP server logs, you should see:
```json
{
  "level": "WARN",
  "message": "infisical unreachable, retrying",
  "attempt": 1,
  "backoff": "1s"
}
{
  "level": "WARN",
  "message": "infisical unreachable, retrying",
  "attempt": 2,
  "backoff": "2s"
}
{
  "level": "INFO",
  "message": "config loaded successfully",
  "attempt": 3
}
```

## Test Invalid Config Handling

To test validation errors, modify the mock server to return invalid configs:

```bash
# Edit scripts/mock-infisical.go or create a test variant
# Return a config with missing required field (e.g., no "host")
go run scripts/mock-infisical.go -port 8000
```

You should see validation error logs:
```json
{
  "level": "ERROR",
  "message": "config validation failed",
  "errors": [
    {"field": "host", "reason": "required"},
    {"field": "port", "reason": "out of range"}
  ]
}
```

## Test Concurrent Requests

While HTTP server is running:

```bash
# Send multiple concurrent requests
for i in {1..10}; do
  curl -X POST http://localhost:6969/notify/email \
    -H "Content-Type: application/json" \
    -d '{"to":"test'$i'@example.com","subject":"Test '$i'","body":"msg"}' &
done
wait
```

The service should handle concurrent requests without issues.

## Expected Files to See

After running, you should see:

```
<project-root>/
├── internal/config/
│   ├── types.go          (Domain entities)
│   ├── service.go        (ConfigService with retry logic)
│   ├── validation.go     (Config validation)
│   ├── watcher.go        (ConfigWatcher — polls Infisical on interval)
│   ├── init.go           (Global service initialization)
│   └── health.go         (Health check handlers)
├── docs/infisical/
│   ├── CONFIG.md         (Setup documentation)
│   ├── TESTING.md        (Detailed testing guide)
│   └── QUICK_TEST.md     (This file)
├── scripts/
│   ├── mock-infisical.go    (Mock server)
│   └── test-local.sh        (Automated test script)
└── cmd/
    └── server/server.go  (HTTP server entry point)
```

## Troubleshooting

### "Connection refused" on port 8000
- Make sure mock Infisical is running: `go run scripts/mock-infisical.go -port 8000`
- Check if port 8000 is in use: `lsof -i :8000`

### "Config service not initialized" error
- Ensure INFISICAL_ADDR and INFISICAL_TOKEN are set
- Check mock server is running and responding: `curl http://localhost:8000/api/v1/secrets`

### Readiness probe returns 503
- Check HTTP server logs for config loading errors
- Verify mock Infisical is returning valid JSON
- Look for validation errors in logs

### Tests pass but "email notification triggered" fails
- Temporal may not be running (that's OK for this test)
- The endpoint itself is working; Temporal integration is separate
- All the config loading logic is working correctly

## Next Steps

Once these tests pass, you can:

1. **Set up real Infisical** — Update `INFISICAL_ADDR` and configure authentication; see [Configuration Reference](../CONFIGURATION.md#authentication) for Machine Identity setup (recommended for production)
2. **Deploy to Cloudflare Tunnel** — Follow setup in CONFIG.md
3. **Test with real email providers** — Add real provider configs to Infisical
4. **Integrate with U2** — Multi-email routing (U2) depends on this config service

## Key Concepts Tested

✓ **Bounded Retry** — Service retries with exponential backoff (1s, 2s, 4s, 8s, 16s)
✓ **Fail Fast** — Non-transient errors fail immediately (auth, 404)
✓ **Validation** — All configs validated before storing
✓ **Thread Safety** — Multiple concurrent requests work correctly
✓ **Health Checks** — Liveness and readiness probes respond correctly
✓ **Logging** — Structured JSON logging with clear error messages
