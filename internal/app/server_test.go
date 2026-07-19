package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"beacon/internal/api"
	"beacon/internal/auth"
	"beacon/internal/channel"
	"beacon/internal/config"
	"beacon/internal/dlq"
	"beacon/internal/notifier"
	"beacon/internal/policy"

	"go.temporal.io/sdk/client"
)

// fakeStarter is a minimal api.WorkflowStarter for testing.
type fakeStarter struct{}

func (f *fakeStarter) ExecuteWorkflow(_ context.Context, _ client.StartWorkflowOptions, _ interface{}, _ ...interface{}) (client.WorkflowRun, error) {
	return nil, nil
}

// fakeDLQ is a minimal api.DLQQuerier for testing.
type fakeDLQ struct{}

func (f *fakeDLQ) QueryFailures(_ context.Context, _ dlq.FailureFilter) ([]*dlq.FailedNotification, error) {
	return []*dlq.FailedNotification{}, nil
}

func (f *fakeDLQ) ReplayWorkflow(_ context.Context, _, _ string) (*dlq.ReplayResult, error) {
	return &dlq.ReplayResult{}, nil
}

// Verify interface compliance at compile time.
var _ api.WorkflowStarter = (*fakeStarter)(nil)
var _ api.DLQQuerier = (*fakeDLQ)(nil)

// buildTestConfigService creates a dev-mode ConfigService without network access.
func buildTestConfigService(t *testing.T) *config.ConfigService {
	t.Helper()
	t.Setenv("DEV_MODE", "true")
	t.Setenv("DEV_SMTP_HOST", "localhost")
	t.Setenv("DEV_SMTP_PORT", "587")
	t.Setenv("DEV_SMTP_NAME", "test")
	t.Setenv("DEV_API_KEY", "bk_k1_devsecret")
	svc, err := config.InitializeConfigService(context.Background(), slog.Default())
	if err != nil {
		t.Fatalf("failed to build dev ConfigService: %v", err)
	}
	return svc
}

// buildTestDeps returns a ServerDeps with all non-nil fields populated.
func buildTestDeps(t *testing.T, dlqSvc api.DLQQuerier) ServerDeps {
	t.Helper()
	health := config.NewHealthChecker()
	cs := buildTestConfigService(t)
	bundle := cs.GetConfig()
	return ServerDeps{
		TemporalClient: &fakeStarter{},
		Channels:       channel.NewRegistry(),
		Providers:      notifier.NewProviderRegistry(bundle),
		AuthRegistry:   auth.NewRegistry(bundle),
		Limiter:        policy.NewMemoryLimiter(nil),
		ConfigService:  cs,
		Health:         health,
		DLQService:     dlqSvc,
		Logger:         slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
}

// ---- ParsePollInterval tests ----

func TestParsePollInterval_EmptyReturnsDefault(t *testing.T) {
	def := 30 * time.Second
	got := ParsePollInterval("", def)
	if got != def {
		t.Errorf("expected %v, got %v", def, got)
	}
}

func TestParsePollInterval_ValidSeconds(t *testing.T) {
	def := 30 * time.Second
	got := ParsePollInterval("60", def)
	want := 60 * time.Second
	if got != want {
		t.Errorf("expected %v, got %v", want, got)
	}
}

func TestParsePollInterval_InvalidString(t *testing.T) {
	def := 15 * time.Second
	got := ParsePollInterval("notanumber", def)
	if got != def {
		t.Errorf("expected default %v for invalid input, got %v", def, got)
	}
}

func TestParsePollInterval_ZeroReturnsDefault(t *testing.T) {
	def := 10 * time.Second
	got := ParsePollInterval("0", def)
	if got != def {
		t.Errorf("expected default %v for zero, got %v", def, got)
	}
}

func TestParsePollInterval_NegativeReturnsDefault(t *testing.T) {
	def := 5 * time.Second
	got := ParsePollInterval("-5", def)
	if got != def {
		t.Errorf("expected default %v for negative, got %v", def, got)
	}
}

// ---- BuildServerMux routing tests ----

func TestBuildServerMux_DLQNil_Returns503(t *testing.T) {
	deps := buildTestDeps(t, nil)
	mux := BuildServerMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/dlq/failed", nil)
	req.Header.Set("Authorization", "Bearer bk_k1_devsecret") // dev-mode key from buildTestConfigService
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/v1/dlq/failed with nil DLQ: expected 503, got %d", rec.Code)
	}
}

// TestBuildServerMux_DLQNil_Unauthenticated401 proves that even when
// DLQService is nil, the "unavailable" v1 DLQ routes still require auth:
// an unauthenticated caller must be rejected with 401 before ever reaching
// the 503 handler, so unauthenticated callers can't probe route availability.
func TestBuildServerMux_DLQNil_Unauthenticated401(t *testing.T) {
	deps := buildTestDeps(t, nil)
	mux := BuildServerMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/dlq/failed", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/v1/dlq/failed with nil DLQ, no auth: expected 401, got %d", rec.Code)
	}
}

func TestBuildServerMux_DLQProvided_Returns200(t *testing.T) {
	deps := buildTestDeps(t, &fakeDLQ{})
	mux := BuildServerMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/dlq/failed", nil)
	req.Header.Set("Authorization", "Bearer bk_k1_devsecret") // dev-mode key from buildTestConfigService
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("/v1/dlq/failed with DLQ: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestBuildServerMux_V1DLQFailed_RequiresAuth proves GET /v1/dlq/failed is
// behind auth.Middleware: an unauthenticated request is rejected before it
// ever reaches DLQHandler.
func TestBuildServerMux_V1DLQFailed_RequiresAuth(t *testing.T) {
	deps := buildTestDeps(t, &fakeDLQ{})
	mux := BuildServerMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/dlq/failed", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/v1/dlq/failed without auth: expected 401, got %d", rec.Code)
	}
}

// TestBuildServerMux_V1DLQReplay_RequiresAuth proves POST
// /v1/dlq/replay/{workflowID} is behind auth.Middleware.
func TestBuildServerMux_V1DLQReplay_RequiresAuth(t *testing.T) {
	deps := buildTestDeps(t, &fakeDLQ{})
	mux := BuildServerMux(deps)

	req := httptest.NewRequest(http.MethodPost, "/v1/dlq/replay/some-id", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/v1/dlq/replay without auth: expected 401, got %d", rec.Code)
	}
}

func TestBuildServerMux_HealthzLive_Returns200(t *testing.T) {
	deps := buildTestDeps(t, nil)
	mux := BuildServerMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/healthz/live", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("/healthz/live: expected 200, got %d", rec.Code)
	}
}

func TestBuildServerMux_DLQReplay_NilDLQ_Returns503(t *testing.T) {
	deps := buildTestDeps(t, nil)
	mux := BuildServerMux(deps)

	req := httptest.NewRequest(http.MethodPost, "/v1/dlq/replay/some-id", nil)
	req.Header.Set("Authorization", "Bearer bk_k1_devsecret") // dev-mode key from buildTestConfigService
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/v1/dlq/replay/ with nil DLQ: expected 503, got %d", rec.Code)
	}
}

func TestBuildServerMux_HealthzReady_Returns200WhenReady(t *testing.T) {
	deps := buildTestDeps(t, nil)
	mux := BuildServerMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/healthz/ready", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("/healthz/ready: expected 200, got %d", rec.Code)
	}
}

func TestBuildServerMux_AdminConfigRefresh_Returns403WhenNoToken(t *testing.T) {
	// Ensure ADMIN_TOKEN is not set so the endpoint returns 403.
	t.Setenv("ADMIN_TOKEN", "")
	deps := buildTestDeps(t, nil)
	mux := BuildServerMux(deps)

	req := httptest.NewRequest(http.MethodPost, "/admin/config/refresh", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("/admin/config/refresh without token: expected 403, got %d", rec.Code)
	}
}

// TestBuildServerMux_V1Notify_RequiresAuth proves that the production wiring in
// BuildServerMux (not just the internal/api test mux) puts /v1/notify/{channel}
// behind auth.Middleware: an unauthenticated request must be rejected before it
// ever reaches NotifyHandler.
func TestBuildServerMux_V1Notify_RequiresAuth(t *testing.T) {
	deps := buildTestDeps(t, nil)
	mux := BuildServerMux(deps)

	req := httptest.NewRequest(http.MethodPost, "/v1/notify/email", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/v1/notify/email without auth: expected 401, got %d", rec.Code)
	}
}
