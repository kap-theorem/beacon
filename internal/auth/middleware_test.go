package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func protected(t *testing.T, reg *Registry) (http.Handler, *[]*Identity) {
	t.Helper()
	var seen []*Identity
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, FromContext(r.Context()))
		w.WriteHeader(http.StatusOK)
	})
	return Middleware(reg)(next), &seen
}

func TestMiddleware_MissingKey401(t *testing.T) {
	h, _ := protected(t, NewRegistry(bundleWithService(true)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/notify/email", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestMiddleware_ValidBearer200(t *testing.T) {
	h, seen := protected(t, NewRegistry(bundleWithService(true)))
	req := httptest.NewRequest("POST", "/v1/notify/email", nil)
	req.Header.Set("Authorization", "Bearer bk_k1_secret123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(*seen) != 1 || (*seen)[0].Service != "billing-api" {
		t.Fatalf("identity not propagated: %+v", *seen)
	}
}

func TestMiddleware_DisabledService403(t *testing.T) {
	h, _ := protected(t, NewRegistry(bundleWithService(false)))
	req := httptest.NewRequest("POST", "/v1/notify/email", nil)
	req.Header.Set("X-API-Key", "bk_k1_secret123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}

func TestMiddleware_AdminToken(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "operator-secret")
	h, seen := protected(t, NewRegistry(nil))
	req := httptest.NewRequest("GET", "/v1/dlq/failed", nil)
	req.Header.Set("Authorization", "Bearer operator-secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if len(*seen) != 1 || !(*seen)[0].Admin {
		t.Fatalf("expected admin identity, got %+v", *seen)
	}
}
