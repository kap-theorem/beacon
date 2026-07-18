package config

import (
	"net/http"
	"net/http/httptest"
	"testing"
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

// --- HandleReady tests ---

func TestHandleReady_GET_NotReady_503(t *testing.T) {
	hc := NewHealthChecker()
	// Default ready=false
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	hc.HandleReady(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
	if body := w.Body.String(); body != "not ready" {
		t.Errorf("expected body 'not ready', got %q", body)
	}
}

func TestHandleReady_GET_Ready_200(t *testing.T) {
	hc := NewHealthChecker()
	hc.SetReady(true)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	hc.HandleReady(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); body != "ready" {
		t.Errorf("expected body 'ready', got %q", body)
	}
}

func TestHandleReady_HEAD_Ready_200(t *testing.T) {
	hc := NewHealthChecker()
	hc.SetReady(true)

	req := httptest.NewRequest(http.MethodHead, "/ready", nil)
	w := httptest.NewRecorder()
	hc.HandleReady(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for HEAD, got %d", w.Code)
	}
}

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

// --- SetReady toggle ---

func TestSetReady_Toggle(t *testing.T) {
	hc := NewHealthChecker()

	// Initially not ready
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	hc.HandleReady(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 initially, got %d", w.Code)
	}

	// Set ready
	hc.SetReady(true)
	req = httptest.NewRequest(http.MethodGet, "/ready", nil)
	w = httptest.NewRecorder()
	hc.HandleReady(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 after SetReady(true), got %d", w.Code)
	}

	// Unset ready
	hc.SetReady(false)
	req = httptest.NewRequest(http.MethodGet, "/ready", nil)
	w = httptest.NewRecorder()
	hc.HandleReady(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 after SetReady(false), got %d", w.Code)
	}
}
