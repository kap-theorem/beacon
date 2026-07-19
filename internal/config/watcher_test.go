package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newWatcherTestServer returns an httptest.Server that serves a different revision
// on every call to the secrets endpoint, triggering a revision bump on each refresh.
func newWatcherTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	cfg := map[string]interface{}{
		"name":      "watcher-provider",
		"provider":  "watcher-provider",
		"host":      "smtp.example.com",
		"port":      587,
		"username":  "user@example.com",
		"password":  "secret",
		"auth_type": "PLAIN",
	}
	b, _ := json.Marshal(cfg)
	providerJSON := string(b)

	type secret struct {
		Key   string `json:"secretKey"`
		Value string `json:"secretValue"`
	}
	resp, _ := json.Marshal(map[string]interface{}{
		"secrets": []secret{{Key: "watcher-provider", Value: providerJSON}},
	})
	body := string(resp)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("secretPath") != "/beacon/providers/email" {
			fmt.Fprint(w, `{"secrets": []}`)
			return
		}
		fmt.Fprint(w, body)
	}))
	return srv
}

// TestConfigWatcher_OnChangeCalledOnRevisionBump creates a real ConfigService backed by
// an httptest Infisical server, seeds it with an initial bundle at revision 0, then
// runs the watcher. Each RefreshConfig call increments the revision inside the service,
// so the first tick will observe newRevision > prevRevision and fire onChange.
func TestConfigWatcher_OnChangeCalledOnRevisionBump(t *testing.T) {
	srv := newWatcherTestServer(t)
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())

	// Prime the service with an initial bundle at revision 0.
	// Store sets current but doesn't touch cs.revision (which starts at 0).
	cs.Store(&ConfigBundle{
		SMTP:      map[string]*SMTPClientConfig{},
		Revision:  0,
		Timestamp: time.Now().UTC(),
	})

	changed := make(chan *ConfigBundle, 1)
	onChange := func(b *ConfigBundle) {
		select {
		case changed <- b:
		default:
		}
	}

	watcher := NewConfigWatcher(cs, 10*time.Millisecond, onChange, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go watcher.Start(ctx)

	select {
	case bundle := <-changed:
		if bundle == nil {
			t.Error("onChange received nil bundle")
		}
		if bundle.Revision <= 0 {
			t.Errorf("expected bumped revision, got %d", bundle.Revision)
		}
		cancel() // stop the watcher
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: onChange was not called within 2s")
	}
}

// TestConfigWatcher_OnChangeNotCalledOnSameRevision verifies that if RefreshConfig
// does not bump the revision (simulated by making the service fail all refreshes),
// onChange is never called.
func TestConfigWatcher_OnChangeNotCalledOnSameRevision(t *testing.T) {
	// Server returns 403 → non-transient → RefreshConfig returns error → no revision bump.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	cs.Store(&ConfigBundle{
		SMTP:      map[string]*SMTPClientConfig{},
		Revision:  1,
		Timestamp: time.Now().UTC(),
	})

	var called int32
	onChange := func(b *ConfigBundle) {
		atomic.AddInt32(&called, 1)
	}

	watcher := NewConfigWatcher(cs, 10*time.Millisecond, onChange, testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	watcher.Start(ctx) // runs until ctx times out

	if n := atomic.LoadInt32(&called); n != 0 {
		t.Errorf("expected onChange never called on error, got %d calls", n)
	}
}

// TestConfigWatcher_DevModeNeverCallsOnChange tests that a dev-mode ConfigService
// (authMethod=="dev") causes RefreshConfig to return ErrDevModeSkip, which the watcher
// handles with a `continue` — onChange is never invoked.
func TestConfigWatcher_DevModeNeverCallsOnChange(t *testing.T) {
	t.Setenv("DEV_MODE", "true")
	t.Setenv("DEV_SMTP_HOST", "localhost")
	t.Setenv("DEV_API_KEY", "bk_k1_devsecret")

	devSvc, err := InitializeConfigService(context.Background(), testLogger())
	if err != nil {
		t.Fatalf("InitializeConfigService in DEV_MODE: %v", err)
	}

	var called int32
	onChange := func(b *ConfigBundle) {
		atomic.AddInt32(&called, 1)
	}

	watcher := NewConfigWatcher(devSvc, 10*time.Millisecond, onChange, testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	watcher.Start(ctx) // runs until ctx times out

	if n := atomic.LoadInt32(&called); n != 0 {
		t.Errorf("expected onChange never called in DEV_MODE, got %d calls", n)
	}
}

// TestConfigWatcher_ContextCancellation ensures Start returns promptly when ctx is cancelled.
func TestConfigWatcher_ContextCancellation(t *testing.T) {
	cs := NewConfigService("http://unused", "proj", "prod", "key", "", "", testLogger())
	cs.Store(&ConfigBundle{
		SMTP:      map[string]*SMTPClientConfig{},
		Revision:  1,
		Timestamp: time.Now().UTC(),
	})

	watcher := NewConfigWatcher(cs, 10*time.Second, func(*ConfigBundle) {}, testLogger())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		watcher.Start(ctx)
		close(done)
	}()

	cancel() // cancel immediately

	select {
	case <-done:
		// Start returned promptly — good
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within 2s after context cancellation")
	}
}

// TestNewConfigWatcher verifies the constructor sets fields correctly.
func TestNewConfigWatcher_Fields(t *testing.T) {
	cs := NewConfigService("http://unused", "proj", "prod", "key", "", "", testLogger())
	interval := 5 * time.Second
	onChange := func(*ConfigBundle) {}
	logger := testLogger()

	w := NewConfigWatcher(cs, interval, onChange, logger)
	if w == nil {
		t.Fatal("expected non-nil ConfigWatcher")
	}
	if w.service != cs {
		t.Error("expected service to be set correctly")
	}
	if w.interval != interval {
		t.Errorf("expected interval %v, got %v", interval, w.interval)
	}
}

// TestConfigWatcher_WarnOnNonDevError verifies that non-ErrDevModeSkip errors from
// RefreshConfig result in a warning log (no panic) and watcher continues.
func TestConfigWatcher_WarnOnNonDevError(t *testing.T) {
	// Use a server that initially fails, then succeeds, to cover the warn branch
	// and also the normal revision-bump path in a single watcher run.
	var callCount int32
	type secret struct {
		Key   string `json:"secretKey"`
		Value string `json:"secretValue"`
	}
	cfg := map[string]interface{}{
		"name":      "warn-provider",
		"provider":  "warn-provider",
		"host":      "smtp.example.com",
		"port":      587,
		"username":  "user@example.com",
		"password":  "secret",
		"auth_type": "PLAIN",
	}
	b, _ := json.Marshal(cfg)
	providerJSON := string(b)
	goodResp, _ := json.Marshal(map[string]interface{}{
		"secrets": []secret{{Key: "warn-provider", Value: string(providerJSON)}},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			// First call: 5xx transient error → warn log, continue
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Subsequent calls: success → revision bump → onChange
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("secretPath") != "/beacon/providers/email" {
			fmt.Fprint(w, `{"secrets": []}`)
			return
		}
		fmt.Fprint(w, string(goodResp))
	}))
	defer srv.Close()

	// Use a short backoffSchedule to avoid long retries inside LoadWithRetry.
	orig := backoffSchedule
	backoffSchedule = []time.Duration{time.Millisecond}
	t.Cleanup(func() { backoffSchedule = orig })

	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	cs.Store(&ConfigBundle{
		SMTP:      map[string]*SMTPClientConfig{},
		Revision:  0,
		Timestamp: time.Now().UTC(),
	})

	changed := make(chan *ConfigBundle, 1)
	onChange := func(b *ConfigBundle) {
		select {
		case changed <- b:
		default:
		}
	}

	watcher := NewConfigWatcher(cs, 10*time.Millisecond, onChange, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go watcher.Start(ctx)

	select {
	case bundle := <-changed:
		if bundle == nil {
			t.Error("onChange received nil bundle")
		}
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: onChange was not called within 3s")
	}
}
