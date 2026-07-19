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

// TestMiddleware_ParsingContract locks down the header-precedence and
// ADMIN_TOKEN-unset behaviors of bearerToken/Middleware.
func TestMiddleware_ParsingContract(t *testing.T) {
	cases := []struct {
		name       string
		adminToken string // "" means unset entirely
		setHeaders func(r *http.Request)
		wantStatus int
		wantAdmin  bool
	}{
		{
			name:       "bearer wins over X-API-Key even when Bearer key is wrong",
			adminToken: "operator-secret",
			setHeaders: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer bk_k1_wrong")
				r.Header.Set("X-API-Key", "bk_k1_secret123")
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "ADMIN_TOKEN unset means no token can authenticate as admin",
			adminToken: "",
			setHeaders: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer some-random-token")
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "non-Bearer Authorization scheme falls through to X-API-Key",
			adminToken: "operator-secret",
			setHeaders: func(r *http.Request) {
				r.Header.Set("Authorization", "Basic xyz")
				r.Header.Set("X-API-Key", "bk_k1_secret123")
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ADMIN_TOKEN", tc.adminToken)
			h, seen := protected(t, NewRegistry(bundleWithService(true)))
			req := httptest.NewRequest("POST", "/v1/notify/email", nil)
			tc.setHeaders(req)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("want %d, got %d: %s", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				if len(*seen) != 1 {
					t.Fatalf("expected identity to be seen, got %+v", *seen)
				}
				if (*seen)[0].Admin != tc.wantAdmin {
					t.Fatalf("admin flag: want %v, got %+v", tc.wantAdmin, (*seen)[0])
				}
			}
		})
	}
}
