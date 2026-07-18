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
	"beacon/internal/config"
	"beacon/internal/dlq"
	"beacon/internal/notifier"

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

func (f *fakeDLQ) ReplayWorkflow(_ context.Context, _ string) (*dlq.ReplayResult, error) {
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
	svc, err := config.InitializeConfigService(context.Background(), slog.Default())
	if err != nil {
		t.Fatalf("failed to build dev ConfigService: %v", err)
	}
	return svc
}

// buildTestRegistry creates an EmailClientRegistry with a single is_default provider.
func buildTestRegistry(t *testing.T) *notifier.EmailClientRegistry {
	t.Helper()
	bundle := &config.ConfigBundle{
		SMTP: map[string]*config.SMTPClientConfig{
			"test": {
				Name:        "test",
				Provider:    "test",
				Host:        "localhost",
				Port:        587,
				IsDefault:   true,
				FromAddress: "noreply@beacon.test",
			},
		},
		Revision:  1,
		Timestamp: time.Now(),
	}
	reg, err := notifier.NewEmailClientRegistry(bundle)
	if err != nil {
		t.Fatalf("failed to build registry: %v", err)
	}
	return reg
}

// buildTestDeps returns a ServerDeps with all non-nil fields populated.
func buildTestDeps(t *testing.T, dlqSvc api.DLQQuerier) ServerDeps {
	t.Helper()
	health := config.NewHealthChecker()
	health.SetReady(true)
	return ServerDeps{
		TemporalClient: &fakeStarter{},
		Registry:       buildTestRegistry(t),
		ConfigService:  buildTestConfigService(t),
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

	req := httptest.NewRequest(http.MethodGet, "/dlq/failed", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/dlq/failed with nil DLQ: expected 503, got %d", rec.Code)
	}
}

func TestBuildServerMux_DLQProvided_Returns200(t *testing.T) {
	deps := buildTestDeps(t, &fakeDLQ{})
	mux := BuildServerMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/dlq/failed", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("/dlq/failed with DLQ: expected 200, got %d", rec.Code)
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

func TestBuildServerMux_NotifyEmail_GetReturns405(t *testing.T) {
	deps := buildTestDeps(t, nil)
	mux := BuildServerMux(deps)

	req := httptest.NewRequest(http.MethodGet, "/notify/email", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("/notify/email GET: expected 405, got %d", rec.Code)
	}
}

func TestBuildServerMux_DLQReplay_NilDLQ_Returns503(t *testing.T) {
	deps := buildTestDeps(t, nil)
	mux := BuildServerMux(deps)

	req := httptest.NewRequest(http.MethodPost, "/dlq/replay/some-id", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/dlq/replay/ with nil DLQ: expected 503, got %d", rec.Code)
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
