package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// testLogger returns a logger that discards all output.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// validProviderJSON builds a complete SMTP config JSON that passes validation.
func validProviderJSON(name string) string {
	cfg := map[string]interface{}{
		"name":      name,
		"provider":  name,
		"host":      "smtp.example.com",
		"port":      587,
		"username":  "user@example.com",
		"password":  "secret",
		"auth_type": "PLAIN",
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}

// infisicalSecretsResponse builds the JSON body Infisical returns for a secrets list.
func infisicalSecretsResponse(keyValues map[string]string) string {
	type secret struct {
		Key   string `json:"secretKey"`
		Value string `json:"secretValue"`
	}
	var secrets []secret
	for k, v := range keyValues {
		secrets = append(secrets, secret{Key: k, Value: v})
	}
	b, _ := json.Marshal(map[string]interface{}{"secrets": secrets})
	return string(b)
}

// authResponse builds the JSON body Infisical returns for a successful auth login.
func authResponse(token string, expiresIn int64) string {
	b, _ := json.Marshal(map[string]interface{}{
		"accessToken": token,
		"expiresIn":   expiresIn,
	})
	return string(b)
}

// infisicalMultiPathResponse returns the JSON body Infisical should return for the
// given secretPath, now that loadFromInfisical fetches three separate paths
// (/beacon/providers/email, /beacon/tenants, /beacon/services) instead of one.
// providerName is served at /beacon/providers/email and referenced by the
// synthesized /beacon/services fixture so ValidateBundleRefs has no unknown-provider
// warning to reconcile.
func infisicalMultiPathResponse(secretPath, providerName string) string {
	switch secretPath {
	case "/beacon/providers/email":
		return infisicalSecretsResponse(map[string]string{
			providerName: validProviderJSON(providerName),
		})
	case "/beacon/tenants":
		return infisicalSecretsResponse(map[string]string{
			"payments": `{"tenant":"payments","name":"Payments"}`,
		})
	case "/beacon/services":
		return infisicalSecretsResponse(map[string]string{
			"billing-api": fmt.Sprintf(`{
  "service": "billing-api",
  "tenant": "payments",
  "enabled": true,
  "keys": [{"id": "k1", "sha256": "%s", "state": "active"}],
  "channels": {
    "email": {
      "providers": ["%s"],
      "default_provider": "%s",
      "from": {"address": "billing@corp.com", "name": "Billing"},
      "rate": {"rpm": 60, "daily": 5000}
    }
  }
}`, testHash, providerName, providerName),
		})
	default:
		return `{"secrets": []}`
	}
}

// --- getAccessToken tests ---

func TestGetAccessToken_ClientSecretPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/universal-auth/login" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, authResponse("test-token", 3600))
			return
		}
		// secrets endpoint
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, infisicalSecretsResponse(map[string]string{
			"p1": validProviderJSON("p1"),
		}))
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "", "clientID", "clientSecret", testLogger())
	tok, err := cs.getAccessToken(context.Background())
	if err != nil {
		t.Fatalf("getAccessToken error: %v", err)
	}
	if tok != "test-token" {
		t.Errorf("expected 'test-token', got %q", tok)
	}
}

func TestGetAccessToken_CachedTokenReuse(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/universal-auth/login" {
			atomic.AddInt32(&callCount, 1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, authResponse("cached-token", 3600))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, infisicalSecretsResponse(nil))
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "", "clientID", "clientSecret", testLogger())

	// First call fetches
	tok1, err := cs.getAccessToken(context.Background())
	if err != nil {
		t.Fatalf("first getAccessToken error: %v", err)
	}
	// Second call should use cache
	tok2, err := cs.getAccessToken(context.Background())
	if err != nil {
		t.Fatalf("second getAccessToken error: %v", err)
	}
	if tok1 != tok2 {
		t.Errorf("cached token mismatch: %q vs %q", tok1, tok2)
	}
	if n := atomic.LoadInt32(&callCount); n != 1 {
		t.Errorf("expected 1 auth call, got %d", n)
	}
}

func TestGetAccessToken_APIKeyPath(t *testing.T) {
	// When using api-key authMethod, getAccessToken returns the apiKey directly (no HTTP call).
	cs := NewConfigService("http://unused", "proj", "prod", "my-api-key", "", "", testLogger())
	tok, err := cs.getAccessToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "my-api-key" {
		t.Errorf("expected api key as token, got %q", tok)
	}
}

func TestGetAccessToken_TokenFallback(t *testing.T) {
	// No clientID/clientSecret, no apiKey → falls back to empty token via apiKey.
	cs := NewConfigService("http://unused", "proj", "prod", "", "", "", testLogger())
	tok, err := cs.getAccessToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "" {
		t.Errorf("expected empty token for no-auth fallback, got %q", tok)
	}
}

func TestGetAccessToken_Non200_4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "", "clientID", "clientSecret", testLogger())
	_, err := cs.getAccessToken(context.Background())
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	// 4xx should NOT be a transient error
	if isTransientError(err) {
		t.Errorf("4xx auth error should not be transient, got: %v", err)
	}
}

func TestGetAccessToken_Non200_5xx_Transient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "", "clientID", "clientSecret", testLogger())
	_, err := cs.getAccessToken(context.Background())
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
	if !isTransientError(err) {
		t.Errorf("5xx auth error should be transient, got: %v", err)
	}
}

func TestGetAccessToken_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "not-json{{{")
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "", "clientID", "clientSecret", testLogger())
	_, err := cs.getAccessToken(context.Background())
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

// --- fetchConfigs tests ---

func TestFetchConfigs_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, infisicalSecretsResponse(map[string]string{
			"p1": validProviderJSON("p1"),
		}))
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	configs, err := cs.fetchConfigs(context.Background(), "/beacon/smtp")
	if err != nil {
		t.Fatalf("fetchConfigs error: %v", err)
	}
	if _, ok := configs["p1"]; !ok {
		t.Errorf("expected key 'p1' in configs, got: %v", configs)
	}
}

func TestFetchConfigs_5xx_Transient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	_, err := cs.fetchConfigs(context.Background(), "/beacon/smtp")
	if err == nil {
		t.Fatal("expected error for 503, got nil")
	}
	if !isTransientError(err) {
		t.Errorf("5xx should be transient, got: %v", err)
	}
}

func TestFetchConfigs_4xx_NonTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	_, err := cs.fetchConfigs(context.Background(), "/beacon/smtp")
	if err == nil {
		t.Fatal("expected error for 403, got nil")
	}
	if isTransientError(err) {
		t.Errorf("4xx should NOT be transient, got: %v", err)
	}
}

func TestFetchConfigs_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "{{not-json}}")
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	_, err := cs.fetchConfigs(context.Background(), "/beacon/smtp")
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

func TestFetchConfigs_408_Transient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout) // 408
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	_, err := cs.fetchConfigs(context.Background(), "/beacon/smtp")
	if err == nil {
		t.Fatal("expected error for 408, got nil")
	}
	if !isTransientError(err) {
		t.Errorf("408 should be transient, got: %v", err)
	}
}

// --- loadFromInfisical tests ---

func TestLoadFromInfisical_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, infisicalMultiPathResponse(r.URL.Query().Get("secretPath"), "p1"))
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	bundle, err := cs.loadFromInfisical(context.Background())
	if err != nil {
		t.Fatalf("loadFromInfisical error: %v", err)
	}
	if _, ok := bundle.SMTP["p1"]; !ok {
		t.Errorf("expected 'p1' in bundle.SMTP, got: %v", bundle.SMTP)
	}
}

func TestLoadFromInfisical_ValidationError(t *testing.T) {
	// Return a secret with invalid config (missing required fields)
	badJSON := `{"name":"","provider":"","host":"","port":0,"auth_type":""}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, infisicalSecretsResponse(map[string]string{
			"bad": badJSON,
		}))
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	_, err := cs.loadFromInfisical(context.Background())
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
}

// --- LoadWithRetry tests ---

func TestLoadWithRetry_SuccessFirstTry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, infisicalMultiPathResponse(r.URL.Query().Get("secretPath"), "p1"))
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	bundle, err := cs.LoadWithRetry(context.Background())
	if err != nil {
		t.Fatalf("LoadWithRetry error: %v", err)
	}
	if bundle == nil {
		t.Fatal("expected non-nil bundle")
	}
	if _, ok := bundle.SMTP["p1"]; !ok {
		t.Errorf("expected 'p1' in bundle, got: %v", bundle.SMTP)
	}
}

func TestLoadWithRetry_NonTransientFailFast(t *testing.T) {
	orig := backoffSchedule
	backoffSchedule = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { backoffSchedule = orig })

	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		// 403 is non-transient
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	_, err := cs.LoadWithRetry(context.Background())
	if err == nil {
		t.Fatal("expected error for non-transient 403")
	}
	// Should fail fast after first attempt
	if n := atomic.LoadInt32(&callCount); n != 1 {
		t.Errorf("expected 1 call for non-transient fail-fast, got %d", n)
	}
}

// TestLoadWithRetry_TransientRetry verifies retry behaviour using client-secret auth:
// the auth endpoint (not the secrets endpoint) returns a raw *TransientError on 5xx,
// and that error IS unwrapped correctly as transient before LoadWithRetry wraps it.
// Note: errors from fetchConfigs/loadFromInfisical are wrapped with fmt.Errorf %w,
// so only the auth-login 5xx path propagates a raw *TransientError to LoadWithRetry.
func TestLoadWithRetry_TransientRetry(t *testing.T) {
	orig := backoffSchedule
	backoffSchedule = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { backoffSchedule = orig })

	var authCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/universal-auth/login" {
			n := atomic.AddInt32(&authCalls, 1)
			if n == 1 {
				// First auth attempt: 5xx → TransientError (unwrapped at LoadWithRetry level)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			// Second auth attempt: success
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, authResponse("tok", 3600))
			return
		}
		// Secrets endpoint: success after auth
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, infisicalMultiPathResponse(r.URL.Query().Get("secretPath"), "p1"))
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "", "clientID", "clientSecret", testLogger())
	b, err := cs.LoadWithRetry(context.Background())
	if err != nil {
		t.Fatalf("expected success after retry, got err: %v", err)
	}
	if _, ok := b.SMTP["p1"]; !ok {
		t.Fatalf("missing provider: %+v", b.SMTP)
	}
}

func TestLoadWithRetry_AllAttemptsError(t *testing.T) {
	orig := backoffSchedule
	backoffSchedule = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { backoffSchedule = orig })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Use auth 5xx (true TransientError) so all retries exhaust
		if r.URL.Path == "/api/v1/auth/universal-auth/login" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "", "clientID", "clientSecret", testLogger())
	_, err := cs.LoadWithRetry(context.Background())
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestLoadWithRetry_CtxCancelledBeforeFirstAttempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, infisicalSecretsResponse(nil))
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := cs.LoadWithRetry(ctx)
	if err == nil {
		t.Fatal("expected context cancelled error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestLoadWithRetry_CtxCancelledDuringBackoff(t *testing.T) {
	orig := backoffSchedule
	// Use a long backoff so context cancellation races the sleep.
	// We use client-secret auth so auth 5xx returns a raw *TransientError (unwrapped),
	// which causes LoadWithRetry to enter the backoff sleep.
	backoffSchedule = []time.Duration{5 * time.Second, 5 * time.Second}
	t.Cleanup(func() { backoffSchedule = orig })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Auth always fails with 5xx → raw TransientError → triggers backoff sleep
		if r.URL.Path == "/api/v1/auth/universal-auth/login" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "", "clientID", "clientSecret", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := cs.LoadWithRetry(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context during backoff")
	}
	// Should be context error (deadline exceeded or cancelled)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("expected context error during backoff, got: %v", err)
	}
}

// --- Store / GetConfig deep-copy test ---

func TestStore_GetConfig_DeepCopy(t *testing.T) {
	cs := NewConfigService("http://unused", "proj", "prod", "key", "", "", testLogger())

	original := &ConfigBundle{
		SMTP: map[string]*SMTPClientConfig{
			"p1": {
				Name:     "p1",
				Provider: "test",
				Host:     "smtp.example.com",
				Port:     587,
			},
		},
		Revision:  1,
		Timestamp: time.Now().UTC(),
	}
	cs.Store(original)

	// Get a snapshot first
	got := cs.GetConfig()
	if got == nil {
		t.Fatal("expected non-nil bundle from GetConfig")
	}
	if got.Revision != 1 {
		t.Errorf("expected revision 1, got %d", got.Revision)
	}
	if _, ok := got.SMTP["p1"]; !ok {
		t.Errorf("expected 'p1' in SMTP map")
	}

	// Verify the snapshot is a separate map from the current stored map.
	// GetConfig returns a new map (shallow copy), so the returned map is not
	// the same reference as cs.current.SMTP. Mutating the RETURNED map should
	// not affect subsequent GetConfig calls.
	got.SMTP["p2"] = &SMTPClientConfig{Name: "p2"}

	// Verify the second call doesn't include the mutation we made to the returned copy
	got2 := cs.GetConfig()
	if _, ok := got2.SMTP["p2"]; ok {
		t.Error("mutation of returned bundle SMTP map should not affect subsequent GetConfig calls")
	}
}

func TestGetConfig_NilCurrent(t *testing.T) {
	cs := NewConfigService("http://unused", "proj", "prod", "key", "", "", testLogger())
	got := cs.GetConfig()
	if got != nil {
		t.Errorf("expected nil for uninitialised service, got: %v", got)
	}
}

// --- GetClientConfig tests ---

func TestGetClientConfig_Found(t *testing.T) {
	cs := NewConfigService("http://unused", "proj", "prod", "key", "", "", testLogger())
	cs.Store(&ConfigBundle{
		SMTP: map[string]*SMTPClientConfig{
			"myProvider": {Name: "myProvider", Host: "smtp.example.com"},
		},
		Revision: 1,
	})

	cfg, err := cs.GetClientConfig("myProvider")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name != "myProvider" {
		t.Errorf("expected name 'myProvider', got %q", cfg.Name)
	}
}

func TestGetClientConfig_NotFound(t *testing.T) {
	cs := NewConfigService("http://unused", "proj", "prod", "key", "", "", testLogger())
	cs.Store(&ConfigBundle{
		SMTP: map[string]*SMTPClientConfig{},
	})

	_, err := cs.GetClientConfig("nonExistent")
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
	if !errors.Is(err, ErrProviderNotFound) {
		t.Errorf("expected ErrProviderNotFound, got: %v", err)
	}
}

func TestGetClientConfig_NotInitialized(t *testing.T) {
	cs := NewConfigService("http://unused", "proj", "prod", "key", "", "", testLogger())
	_, err := cs.GetClientConfig("anyProvider")
	if err == nil {
		t.Fatal("expected error for uninitialised service")
	}
	if !errors.Is(err, ErrConfigNotInitialized) {
		t.Errorf("expected ErrConfigNotInitialized, got: %v", err)
	}
}

// --- RefreshConfig tests ---

func TestRefreshConfig_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, infisicalMultiPathResponse(r.URL.Query().Get("secretPath"), "p1"))
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	// Store an initial bundle so previous gets set
	cs.Store(&ConfigBundle{SMTP: map[string]*SMTPClientConfig{}, Revision: 0})

	err := cs.RefreshConfig(context.Background())
	if err != nil {
		t.Fatalf("RefreshConfig error: %v", err)
	}
	cfg := cs.GetConfig()
	if cfg == nil {
		t.Fatal("expected config bundle after refresh")
	}
	if _, ok := cfg.SMTP["p1"]; !ok {
		t.Errorf("expected 'p1' after refresh, got: %v", cfg.SMTP)
	}
}

func TestRefreshConfig_FailureRevertsToPreivous(t *testing.T) {
	orig := backoffSchedule
	backoffSchedule = []time.Duration{time.Millisecond}
	t.Cleanup(func() { backoffSchedule = orig })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // non-transient failure
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())

	// Set up current and previous
	prevBundle := &ConfigBundle{
		SMTP: map[string]*SMTPClientConfig{
			"prev": {Name: "prev"},
		},
		Revision: 1,
	}
	cs.Store(prevBundle) // sets current
	// Store again to shift current → previous
	cs.Store(&ConfigBundle{
		SMTP:     map[string]*SMTPClientConfig{"curr": {Name: "curr"}},
		Revision: 2,
	})

	err := cs.RefreshConfig(context.Background())
	if err == nil {
		t.Fatal("expected error from failed refresh")
	}
	// After failure, current should be reverted to previous
	cfg := cs.GetConfig()
	if cfg == nil {
		t.Fatal("expected config bundle after failed refresh revert")
	}
}

func TestRefreshConfig_FailureNoPrevious(t *testing.T) {
	orig := backoffSchedule
	backoffSchedule = []time.Duration{time.Millisecond}
	t.Cleanup(func() { backoffSchedule = orig })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	// No previous bundle stored
	err := cs.RefreshConfig(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- GetRevision tests ---

func TestGetRevision(t *testing.T) {
	cs := NewConfigService("http://unused", "proj", "prod", "key", "", "", testLogger())
	if rev := cs.GetRevision(); rev != 0 {
		t.Errorf("expected initial revision 0, got %d", rev)
	}
}

// --- TransientError tests ---

func TestTransientError(t *testing.T) {
	err := &TransientError{msg: "server error"}
	if err.Error() != "server error" {
		t.Errorf("expected 'server error', got %q", err.Error())
	}
	if !isTransientError(err) {
		t.Error("TransientError should be detected as transient")
	}
	if isTransientError(fmt.Errorf("regular error")) {
		t.Error("regular error should not be transient")
	}
}

// --- client-secret auth flow through fetchConfigs ---

func TestFetchConfigs_ClientSecretAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/auth/universal-auth/login" {
			fmt.Fprint(w, authResponse("bearer-token", 3600))
			return
		}
		fmt.Fprint(w, infisicalSecretsResponse(map[string]string{
			"p1": validProviderJSON("p1"),
		}))
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "", "cID", "cSecret", testLogger())
	configs, err := cs.fetchConfigs(context.Background(), "/beacon/smtp")
	if err != nil {
		t.Fatalf("fetchConfigs with client-secret auth error: %v", err)
	}
	if _, ok := configs["p1"]; !ok {
		t.Errorf("expected 'p1' in configs")
	}
}

// --- Ensure strconv is used ---
var _ = strconv.Itoa
