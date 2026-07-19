package config

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// --- HandleLive tests ---

func TestHandleLive_GET_200(t *testing.T) {
	hc := NewHealthChecker()
	req := httptest.NewRequest(http.MethodGet, "/live", nil)
	w := httptest.NewRecorder()
	hc.HandleLive(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); body != "ok" {
		t.Errorf("expected body 'ok', got %q", body)
	}
}

func TestHandleLive_HEAD_200(t *testing.T) {
	hc := NewHealthChecker()
	req := httptest.NewRequest(http.MethodHead, "/live", nil)
	w := httptest.NewRecorder()
	hc.HandleLive(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for HEAD, got %d", w.Code)
	}
}

func TestHandleLive_POST_405(t *testing.T) {
	hc := NewHealthChecker()
	req := httptest.NewRequest(http.MethodPost, "/live", nil)
	w := httptest.NewRecorder()
	hc.HandleLive(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
	allow := w.Header().Get("Allow")
	if allow == "" {
		t.Error("expected Allow header to be set on 405")
	}
}

func TestHandleLive_PUT_405(t *testing.T) {
	hc := NewHealthChecker()
	req := httptest.NewRequest(http.MethodPut, "/live", nil)
	w := httptest.NewRecorder()
	hc.HandleLive(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for PUT, got %d", w.Code)
	}
}

// --- HandleReady method tests ---

func TestHandleReady_POST_405(t *testing.T) {
	hc := NewHealthChecker()
	req := httptest.NewRequest(http.MethodPost, "/ready", nil)
	w := httptest.NewRecorder()
	hc.HandleReady(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
	allow := w.Header().Get("Allow")
	if allow == "" {
		t.Error("expected Allow header to be set on 405")
	}
}

func TestHandleReady_DELETE_405(t *testing.T) {
	hc := NewHealthChecker()
	req := httptest.NewRequest(http.MethodDelete, "/ready", nil)
	w := httptest.NewRecorder()
	hc.HandleReady(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for DELETE, got %d", w.Code)
	}
}

// --- HandleReady readiness-check tests ---

func TestHandleReady_AllChecksPass(t *testing.T) {
	hc := NewHealthChecker(ReadinessCheck{Name: "ok", Fn: func(ctx context.Context) error { return nil }})
	rec := httptest.NewRecorder()
	hc.HandleReady(rec, httptest.NewRequest("GET", "/healthz/ready", nil))
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}

func TestHandleReady_FailingCheck503(t *testing.T) {
	hc := NewHealthChecker(ReadinessCheck{Name: "temporal", Fn: func(ctx context.Context) error {
		return fmt.Errorf("unreachable")
	}})
	rec := httptest.NewRecorder()
	hc.HandleReady(rec, httptest.NewRequest("GET", "/healthz/ready", nil))
	if rec.Code != 503 {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}

func TestHandleReady_ResultCached(t *testing.T) {
	calls := 0
	hc := NewHealthChecker(ReadinessCheck{Name: "count", Fn: func(ctx context.Context) error {
		calls++
		return nil
	}})
	hc.ttl = time.Hour
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		hc.HandleReady(rec, httptest.NewRequest("GET", "/healthz/ready", nil))
	}
	if calls != 1 {
		t.Fatalf("want 1 evaluation (cached), got %d", calls)
	}
}

func TestHandleReady_CallerCancellationDoesNotPoisonCache(t *testing.T) {
	hc := NewHealthChecker(ReadinessCheck{Name: "slow", Fn: func(ctx context.Context) error {
		select {
		case <-time.After(100 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})

	// First prober arrives with an already-cancelled context (simulating a
	// short-timeout client, e.g. kubelet, that disconnected mid-check).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", "/healthz/ready", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	hc.HandleReady(rec, req)
	if rec.Code != 200 {
		t.Fatalf("caller cancellation must not affect shared evaluation: want 200, got %d", rec.Code)
	}

	// A normal, uncancelled request immediately after must also see 200 —
	// the cached result must not have been poisoned by the first caller's
	// cancelled context.
	rec2 := httptest.NewRecorder()
	hc.HandleReady(rec2, httptest.NewRequest("GET", "/healthz/ready", nil))
	if rec2.Code != 200 {
		t.Fatalf("want 200 for follow-up request, got %d", rec2.Code)
	}
}
