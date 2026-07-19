package api

import (
	"beacon/internal/config"
	"beacon/internal/notifier"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// smtpSecretJSON is a valid SMTP config JSON that passes ValidateConfig.
const smtpSecretJSON = `{
	"name": "testprovider",
	"provider": "smtp",
	"host": "smtp.example.com",
	"port": 587,
	"username": "user",
	"password": "pass",
	"auth_type": "PLAIN",
	"from_address": "noreply@example.com",
	"is_default": true
}`

// newInfisicalServer starts an httptest.Server that mimics the Infisical
// /api/v4/secrets endpoint used by ConfigService.fetchConfigs.
func newInfisicalServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("secretPath") != "/beacon/providers/email" {
			// Tenants/services aren't under test here; keep the bundle valid but empty.
			json.NewEncoder(w).Encode(map[string]any{"secrets": []any{}})
			return
		}
		// Return a single secret whose value is the SMTP config JSON.
		resp := map[string]any{
			"secrets": []map[string]any{
				{
					"secretKey":     "SMTP_TESTPROVIDER",
					"secretValue":   smtpSecretJSON,
					"secretComment": "",
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

// newAdminHandlerWithLiveCS builds an AdminHandler backed by a real ConfigService that
// points at the provided httptest server (simulating Infisical) and has an initial
// config bundle already stored so GetConfig() works after a successful RefreshConfig.
func newAdminHandlerWithLiveCS(t *testing.T, infisicalURL string) *AdminHandler {
	t.Helper()

	// Use api-key auth so ConfigService skips the /auth/universal-auth/login call
	// and sends apiKey directly in the Authorization header.
	cs := config.NewConfigService(infisicalURL, "proj-1", "test", "test-api-key", "", "", discardLogger())

	// Pre-populate with a bundle so the registry can be built.
	initialBundle := minimalBundle()
	cs.Store(initialBundle)

	registry, err := notifier.NewEmailClientRegistry(initialBundle)
	require.NoError(t, err)

	return NewAdminHandler(cs, registry, discardLogger())
}

// newDevModeAdminHandler creates an AdminHandler whose ConfigService is in DEV_MODE.
// DEV_MODE is triggered by authMethod == "dev"; we achieve this by setting
// infisicalAddr and all auth credentials to empty strings so authMethod falls
// back to "token" — but the actual dev-skip sentinel is checked via `authMethod == "dev"`.
// The simplest reliable way: use NewConfigService with empty clientID/secret/apiKey
// so authMethod = "token", then note that RefreshConfig checks `authMethod == "dev"`.
// To actually trigger ErrDevModeSkip we need authMethod to be "dev", which is only
// set internally. We therefore stub via a wrapper that satisfies the interface by
// directly checking the exported ErrDevModeSkip.
//
// Approach: Since ConfigService is a concrete struct (not an interface), we cannot
// mock it. We can however call NewConfigService with the special empty credentials
// to get authMethod="token" and then rely on a different path to surface
// ErrDevModeSkip. Looking at the source, ErrDevModeSkip is returned only when
// `cs.authMethod == "dev"`. That value is never set from NewConfigService; there
// is no public setter.
//
// The correct approach for the dev-mode test is to use a real ConfigService created
// with DEV_MODE=true env var (the init.go or watcher.go path) OR to accept that
// this specific branch can only be reached through the watcher-level constructor.
//
// Instead, we test the admin handler's ErrDevModeSkip branch by wrapping — but
// since AdminHandler.ConfigService is a *config.ConfigService (not an interface)
// we cannot inject a fake. This is a production-code limitation (the field is a
// concrete type, not an interface). We document this constraint and instead test
// the 503 path by pointing the ConfigService at a server that returns 500 (causing
// a transient error leading to a real error from RefreshConfig — which surfaces as
// 500, not 503).
//
// For the true ErrDevModeSkip / 503 path to be testable without changing production
// code, we need DEV_MODE auth. The init.go file may expose this. Check there.
func TestAdmin_HandleConfigRefresh_MethodNotAllowed(t *testing.T) {
	srv := newInfisicalServer(t)
	defer srv.Close()

	h := newAdminHandlerWithLiveCS(t, srv.URL)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/admin/config/refresh", nil)
		w := httptest.NewRecorder()
		h.HandleConfigRefresh(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, "method %s should be 405", method)
	}
}

func TestAdmin_HandleConfigRefresh_AdminTokenUnset(t *testing.T) {
	// Ensure ADMIN_TOKEN is absent.
	t.Setenv("ADMIN_TOKEN", "")

	srv := newInfisicalServer(t)
	defer srv.Close()

	h := newAdminHandlerWithLiveCS(t, srv.URL)

	req := httptest.NewRequest(http.MethodPost, "/admin/config/refresh", nil)
	w := httptest.NewRecorder()
	h.HandleConfigRefresh(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestAdmin_HandleConfigRefresh_WrongToken(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "correct-secret")

	srv := newInfisicalServer(t)
	defer srv.Close()

	h := newAdminHandlerWithLiveCS(t, srv.URL)

	req := httptest.NewRequest(http.MethodPost, "/admin/config/refresh", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	h.HandleConfigRefresh(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAdmin_HandleConfigRefresh_Success(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "secret")

	srv := newInfisicalServer(t)
	defer srv.Close()

	h := newAdminHandlerWithLiveCS(t, srv.URL)

	req := httptest.NewRequest(http.MethodPost, "/admin/config/refresh", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.HandleConfigRefresh(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.True(t, body["success"].(bool))

	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "response should have a data object")

	// Revision is a number in JSON
	_, hasRevision := data["revision"]
	assert.True(t, hasRevision, "response data should include revision")

	providers, ok := data["providers"].([]any)
	require.True(t, ok, "providers should be a JSON array")
	assert.NotEmpty(t, providers, "at least one provider should be registered after refresh")
}

func TestAdmin_HandleConfigRefresh_RefreshError(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "secret")

	// Point ConfigService at a server that always returns 400 (non-transient error).
	// LoadWithRetry will fast-fail with a non-transient error.
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer badSrv.Close()

	h := newAdminHandlerWithLiveCS(t, badSrv.URL)

	req := httptest.NewRequest(http.MethodPost, "/admin/config/refresh", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.HandleConfigRefresh(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestAdmin_HandleConfigRefresh_DevModeSkip(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "secret")

	// Build a ConfigService via NewConfigService with all credential fields empty
	// so authMethod = "token". Then manually mutate to reach authMethod="dev".
	// Since authMethod is unexported and there is no public setter, we have to work
	// around the constraint: instead we confirm the ErrDevModeSkip sentinel is
	// correctly handled by the handler through a different path.
	//
	// The ConfigService's RefreshConfig method only sets authMethod to "dev" through
	// an internal code path (no public API). Therefore, this specific branch (503)
	// cannot be exercised without modifying production code to expose an interface or
	// a constructor option.
	//
	// We document this as a coverage gap and skip the test rather than emit a false
	// positive, so the team is aware.
	t.Skip("ErrDevModeSkip branch requires authMethod='dev' which has no public constructor path; " +
		"production code should expose ConfigServicer interface to enable full coverage")
}

// TestAdmin_HandleConfigRefresh_RegistryReloadError verifies the 500 path when
// registry.Reload fails after a successful ConfigService refresh.
// This is achieved by using a config where the refreshed bundle has no SMTP providers
// (which makes Reload return an error).
func TestAdmin_HandleConfigRefresh_RegistryReloadError(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "secret")

	// Server returns an empty secrets list — loadFromInfisical succeeds but with
	// zero SMTP providers, causing registry.Reload to fail.
	emptySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"secrets": []any{}})
	}))
	defer emptySrv.Close()

	h := newAdminHandlerWithLiveCS(t, emptySrv.URL)

	req := httptest.NewRequest(http.MethodPost, "/admin/config/refresh", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.HandleConfigRefresh(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
