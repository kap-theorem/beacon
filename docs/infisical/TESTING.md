# Manual Testing Guide — U1: Infisical Config Integration

## Prerequisites

```bash
# Install dependencies
go get ./...

# Verify builds
go build ./cmd/server
go build ./cmd/email_worker
```

## Test 1: Config Service Unit Tests (Validation)

### Test Structural Validation

```bash
go test -v ./internal/config -run TestValidation
```

Create a test file to verify:

```go
// internal/config/validation_test.go
package config

import (
	"testing"
	"time"
)

func TestValidationStructural(t *testing.T) {
	tests := []struct {
		name    string
		rawJSON string
		valid   bool
		errMsg  string
	}{
		{
			name:    "valid config",
			rawJSON: `{"name":"sendgrid","provider":"sendgrid","host":"smtp.sendgrid.net","port":587,"username":"apikey","password":"sg_key","auth_type":"PLAIN"}`,
			valid:   true,
		},
		{
			name:    "missing required field (host)",
			rawJSON: `{"name":"sendgrid","provider":"sendgrid","port":587,"username":"apikey","auth_type":"PLAIN"}`,
			valid:   false,
			errMsg:  "host: required",
		},
		{
			name:    "invalid JSON",
			rawJSON: `{invalid json}`,
			valid:   false,
			errMsg:  "invalid JSON",
		},
		{
			name:    "port out of range",
			rawJSON: `{"name":"s","provider":"s","host":"smtp.example.com","port":99999,"username":"u","auth_type":"PLAIN"}`,
			valid:   false,
			errMsg:  "port: must be between",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ValidateConfig(tt.rawJSON)
			if tt.valid {
				if err != nil {
					t.Errorf("expected valid, got error: %v", err)
				}
				if cfg == nil {
					t.Errorf("expected config, got nil")
				}
			} else {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
			}
		})
	}
}
```

## Test 2: Mock Infisical Server

Create a local mock Infisical server to test config loading:

```bash
# Create a test script
cat > /tmp/mock_infisical.go << 'EOF'
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("/api/v4/secrets", func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"secrets": []map[string]string{
				{
					"secretKey": "sendgrid",
					"secretValue": `{
						"name":"sendgrid",
						"provider":"sendgrid",
						"host":"smtp.sendgrid.net",
						"port":587,
						"username":"apikey",
						"password":"SG.test-key",
						"auth_type":"PLAIN",
						"tls":{"enabled":true,"server_name":"smtp.sendgrid.net"},
						"timeout":"30s",
						"max_retries":3,
						"max_per_hour":0
					}`,
				},
				{
					"secretKey": "mailgun",
					"secretValue": `{
						"name":"mailgun",
						"provider":"mailgun",
						"host":"smtp.mailgun.org",
						"port":587,
						"username":"postmaster@example.com",
						"password":"mg-key",
						"auth_type":"PLAIN",
						"tls":{"enabled":true,"server_name":"smtp.mailgun.org"}
					}`,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	})

	fmt.Println("Mock Infisical server running on :8000")
	http.ListenAndServe(":8000", nil)
}
EOF

# Run the mock server
go run /tmp/mock_infisical.go
```

## Test 3: ConfigService Direct Test

In another terminal:

```bash
cat > /tmp/test_config.go << 'EOF'
package main

import (
	"beacon/internal/config"
	"context"
	"log/slog"
	"os"
	"time"
)

func main() {
	// Set environment for mock server
	os.Setenv("INFISICAL_ADDR", "http://localhost:8000")
	os.Setenv("INFISICAL_TOKEN", "test-token")

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Test 1: Load config with retry
	logger.Info("Test 1: Loading config from Infisical")
	service := config.NewConfigService("http://localhost:8000", "test-token", logger)
	bundle, err := service.LoadWithRetry(ctx)
	if err != nil {
		logger.Error("Failed to load config", slog.Any("error", err))
		return
	}

	logger.Info("Config loaded successfully",
		slog.Int("providers", len(bundle.SMTP)),
		slog.Int64("revision", bundle.Revision),
	)

	// Test 2: Store and retrieve
	logger.Info("Test 2: Storing config")
	service.Store(bundle)

	// Test 3: Get specific provider
	logger.Info("Test 3: Retrieving specific provider")
	cfg, err := service.GetClientConfig("sendgrid")
	if err != nil {
		logger.Error("Failed to get client config", slog.Any("error", err))
		return
	}
	logger.Info("Retrieved provider config", slog.Any("config", cfg.LogSafe()))

	// Test 4: Cache age
	logger.Info("Test 4: Checking cache age")
	age := service.GetCacheAge()
	logger.Info("Cache age", slog.Duration("age", age))

	logger.Info("All tests passed!")
}
EOF

# Run the test
go run /tmp/test_config.go
```

## Test 4: HTTP Server Health Checks

Start the HTTP server in another terminal:

```bash
export INFISICAL_ADDR="http://localhost:8000"
export INFISICAL_TOKEN="test-token"
export TEMPORAL_ADDRESS="localhost:7233"
export TEMPORAL_NAMESPACE="default"

go run ./cmd/server/main.go
```

In yet another terminal, test the health endpoints:

```bash
# Test 1: Liveness probe (should return 200 immediately)
echo "Liveness probe:"
curl -i http://localhost:6969/healthz/live

# Test 2: Readiness probe (should return 200 once config is loaded)
echo -e "\nReadiness probe:"
curl -i http://localhost:6969/healthz/ready

# Test 3: Send email endpoint (should work if Temporal is running)
echo -e "\nSend email:"
curl -X POST http://localhost:6969/notify/email \
  -H "Content-Type: application/json" \
  -d '{
    "to": "test@example.com",
    "subject": "Test Email",
    "body": "This is a test email"
  }'
```

## Test 5: Config Validation Errors

Test with invalid configs in the mock server:

```go
// Modify /tmp/mock_infisical.go to include invalid config
{
	"secretKey": "invalid",
	"secretValue": `{
		"name":"invalid",
		"port": 99999
	}`,
}
```

Expected error in logs:
```json
{
  "level": "ERROR",
  "message": "config validation failed",
  "errors": [
    {"field": "host", "reason": "required"},
    {"field": "port", "reason": "must be between 1 and 65535"}
  ]
}
```

## Test 6: Retry Logic

Test transient error handling:

```bash
# Create a mock server that fails first 2 times, succeeds on 3rd
cat > /tmp/mock_infisical_retry.go << 'EOF'
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
)

var counter int64

func main() {
	http.HandleFunc("/api/v4/secrets", func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt64(&counter, 1)
		
		if count < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "Service temporarily unavailable")
			return
		}

		response := map[string]interface{}{
			"secrets": []map[string]string{
				{
					"secretKey": "sendgrid",
					"secretValue": `{"name":"sendgrid","provider":"sendgrid","host":"smtp.sendgrid.net","port":587,"username":"apikey","password":"key","auth_type":"PLAIN"}`,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		fmt.Printf("Request %d succeeded\n", count)
	})

	fmt.Println("Retry test server on :8001")
	http.ListenAndServe(":8001", nil)
}
EOF

# Run retry test
INFISICAL_ADDR="http://localhost:8001" go run /tmp/test_config.go
```

Expected logs:
```json
{
  "level": "WARN",
  "message": "infisical unreachable, retrying",
  "error": "HTTP 503",
  "attempt": 1,
  "backoff": "1s"
}
{
  "level": "WARN",
  "message": "infisical unreachable, retrying",
  "error": "HTTP 503",
  "attempt": 2,
  "backoff": "2s"
}
{
  "level": "INFO",
  "message": "config loaded successfully",
  "providers": 1,
  "attempt": 3
}
```

## Test 7: Non-Transient Errors (Fail Fast)

Test auth error (should fail immediately without retry):

```bash
# Create mock server that returns 401 Unauthorized
cat > /tmp/mock_infisical_auth.go << 'EOF'
package main

import (
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("/api/v4/secrets", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "Invalid token")
	})

	fmt.Println("Auth test server on :8002")
	http.ListenAndServe(":8002", nil)
}
EOF

# Test with invalid token
INFISICAL_ADDR="http://localhost:8002" go run /tmp/test_config.go
```

Expected: Fails immediately with error (no retries)

## Test 8: Config Refresh (Fallback)

Test fallback to previous config on refresh failure:

```go
// Add test to show fallback behavior
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

service := config.NewConfigService("http://localhost:8000", "token", logger)

// Load initial config
bundle1, _ := service.LoadWithRetry(ctx)
service.Store(bundle1)
logger.Info("Initial config loaded", slog.Int64("revision", bundle1.Revision))

// Change mock server to return invalid config
// (would need to modify mock server)

// Try to refresh (should fail and revert to previous)
err := service.RefreshConfig(ctx)
if err != nil {
	logger.Warn("Refresh failed, reverted to previous", slog.Any("error", err))
}

// Verify we're still using old config
current := service.GetConfig()
logger.Info("Current config after failed refresh", slog.Int64("revision", current.Revision))
```

## Test 9: Concurrent Access (Thread Safety)

Test RWMutex protection:

```go
package main

import (
	"beacon/internal/config"
	"context"
	"log/slog"
	"os"
	"sync"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	service := config.NewConfigService("http://localhost:8000", "token", logger)

	// Load initial config
	ctx, _ := context.WithTimeout(context.Background(), 10*time.Second)
	bundle, _ := service.LoadWithRetry(ctx)
	service.Store(bundle)

	var wg sync.WaitGroup
	done := make(chan bool)

	// 10 goroutines reading config simultaneously
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					cfg := service.GetConfig()
					if cfg != nil {
						_, _ = service.GetClientConfig("sendgrid")
					}
				}
			}
		}(i)
	}

	time.Sleep(2 * time.Second)
	close(done)
	wg.Wait()

	logger.Info("Concurrent access test passed")
}
```

## Test 10: Email Worker Startup

```bash
# In a terminal with mock Infisical running
export INFISICAL_ADDR="http://localhost:8000"
export INFISICAL_TOKEN="test-token"
export TEMPORAL_ADDRESS="localhost:7233"
export TEMPORAL_NAMESPACE="default"

go run ./cmd/email_worker/main.go
```

Expected log output:
```json
{
  "level": "INFO",
  "message": "config service initialized",
  "providers": 2,
  "revision": 1
}
{
  "level": "INFO",
  "message": "email worker starting",
  "task_queue": "email-notifications"
}
```

## Cleanup

Kill mock servers:

```bash
pkill -f "mock_infisical"
```

## Summary

| Test | Command | Expected Result |
|------|---------|-----------------|
| Unit Tests | `go test ./internal/config` | All validation tests pass |
| Mock Server | `go run /tmp/mock_infisical.go` | Server on :8000 |
| Config Load | `go run /tmp/test_config.go` | Config loads, providers retrieved |
| Health Live | `curl http://localhost:6969/healthz/live` | 200 OK |
| Health Ready | `curl http://localhost:6969/healthz/ready` | 200 OK |
| Retry Logic | 503 Server | Retries with backoff, succeeds |
| Fail Fast | 401 Server | Fails immediately (no retries) |
| Fallback | Refresh with invalid config | Reverts to previous config |
| Concurrent | Multiple readers | No race conditions |
| Worker Startup | `go run ./cmd/email_worker/main.go` | Config loads, worker starts |

