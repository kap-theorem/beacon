package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"beacon/internal/auth"
	"beacon/internal/channel"
	"beacon/internal/config"
	"beacon/internal/models"
	"beacon/internal/notifier"
	"beacon/internal/policy"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

// notifyFakeRun/notifyFakeStarter: minimal WorkflowStarter for handler tests.
type notifyFakeRun struct{ id, runID string }

func (f notifyFakeRun) GetID() string                                       { return f.id }
func (f notifyFakeRun) GetRunID() string                                    { return f.runID }
func (f notifyFakeRun) Get(ctx context.Context, valuePtr interface{}) error { return nil }
func (f notifyFakeRun) GetWithOptions(ctx context.Context, valuePtr interface{}, options client.WorkflowRunGetOptions) error {
	return nil
}

type notifyFakeStarter struct {
	lastOptions client.StartWorkflowOptions
	lastArg     interface{}
	err         error
}

func (f *notifyFakeStarter) ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (client.WorkflowRun, error) {
	f.lastOptions = options
	if len(args) > 0 {
		f.lastArg = args[0]
	}
	if f.err != nil {
		return nil, f.err
	}
	return notifyFakeRun{id: options.ID, runID: "run-1"}, nil
}

// testBundle returns the shared fixture bundle: "billing-api" (key
// bk_k1_secret123) may send email via sendgrid or the unconfigured "ses"
// provider, and "metrics-agent" (key bk_m1_metricsecret) is enabled but has
// no "email" entry in its Channels map.
func testBundle() *config.ConfigBundle {
	key := "bk_k1_secret123"
	metricsKey := "bk_m1_metricsecret"
	return &config.ConfigBundle{
		SMTP: map[string]*config.SMTPClientConfig{"sendgrid": {Name: "sendgrid"}},
		Services: map[string]*config.ServiceConfig{
			"billing-api": {
				Service: "billing-api", Tenant: "payments", Enabled: true,
				Keys: []config.KeyEntry{{ID: "k1", SHA256: auth.HashKey(key), State: "active"}},
				Channels: map[string]*config.ChannelPolicy{
					// "ses" is allowed by policy but not backed by any configured
					// SMTP provider, so requesting it exercises the 503
					// "not configured" branch distinct from the 403 policy check.
					"email": {Providers: []string{"sendgrid", "ses"}, DefaultProvider: "sendgrid",
						From: &config.FromIdentity{Address: "billing@corp.com", Name: "Billing"},
						Rate: config.RateConfig{RPM: 60, Daily: 5000}},
				},
			},
			// metrics-agent is a registered, enabled service with no "email"
			// entry in its Channels map, exercising the "channel not enabled
			// for this service" 403 branch (distinct from unknown-channel 404).
			"metrics-agent": {
				Service: "metrics-agent", Tenant: "obs", Enabled: true,
				Keys:     []config.KeyEntry{{ID: "m1", SHA256: auth.HashKey(metricsKey), State: "active"}},
				Channels: map[string]*config.ChannelPolicy{},
			},
		},
	}
}

// buildNotifyMux wires the NotifyHandler behind auth.Middleware for a given
// bundle and starter (starter may be nil, e.g. to test the Temporal-unavailable
// path — a bare untyped nil keeps the WorkflowStarter interface itself nil,
// unlike passing a typed nil *notifyFakeStarter).
func buildNotifyMux(t *testing.T, bundle *config.ConfigBundle, starter WorkflowStarter, limiter policy.RateLimiter) (http.Handler, *auth.Registry) {
	t.Helper()
	reg := auth.NewRegistry(bundle)
	h := &NotifyHandler{
		TemporalClient: starter,
		Channels:       channel.NewRegistry(),
		Providers:      notifier.NewProviderRegistry(bundle),
		Limiter:        limiter,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/notify/{channel}", auth.Middleware(reg)(http.HandlerFunc(h.Handle)))
	return mux, reg
}

func testMux(t *testing.T, starter *notifyFakeStarter, limiter policy.RateLimiter) (http.Handler, *auth.Registry) {
	t.Helper()
	return buildNotifyMux(t, testBundle(), starter, limiter)
}

func post(t *testing.T, mux http.Handler, path, key, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

const goodBody = `{"to":"a@b.com","subject":"s","body":"b"}`

func TestNotify_HappyPath(t *testing.T) {
	starter := &notifyFakeStarter{}
	mux, _ := testMux(t, starter, policy.NewMemoryLimiter(nil))
	rec := post(t, mux, "/v1/notify/email", "bk_k1_secret123", goodBody, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if starter.lastOptions.TaskQueue != "email-sendgrid-queue" {
		t.Fatalf("task queue: %q", starter.lastOptions.TaskQueue)
	}
	if starter.lastOptions.Memo["tenant"] != "payments" {
		t.Fatalf("memo: %+v", starter.lastOptions.Memo)
	}
	var resp struct {
		Data map[string]any `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Data["provider"] != "sendgrid" || resp.Data["duplicate"] != false {
		t.Fatalf("response data: %+v", resp.Data)
	}
}

func TestNotify_FromLockAppliedFromPolicy(t *testing.T) {
	starter := &notifyFakeStarter{}
	mux, _ := testMux(t, starter, policy.NewMemoryLimiter(nil))
	post(t, mux, "/v1/notify/email", "bk_k1_secret123", goodBody, nil)
	n, ok := starter.lastArg.(*models.Notification)
	if !ok {
		t.Fatalf("workflow arg is %T, want *models.Notification", starter.lastArg)
	}
	if n.Service != "billing-api" || n.Tenant != "payments" {
		t.Fatalf("identity not stamped on envelope: %+v", n)
	}
	if n.Email.FromAddress != "billing@corp.com" || n.Email.FromName != "Billing" {
		t.Fatalf("policy From not injected: %+v", n.Email)
	}
}

func TestNotify_NoKey401(t *testing.T) {
	mux, _ := testMux(t, &notifyFakeStarter{}, policy.NewMemoryLimiter(nil))
	rec := post(t, mux, "/v1/notify/email", "", goodBody, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestNotify_UnknownChannel404(t *testing.T) {
	mux, _ := testMux(t, &notifyFakeStarter{}, policy.NewMemoryLimiter(nil))
	rec := post(t, mux, "/v1/notify/sms", "bk_k1_secret123", `{"to":"+1555","body":"x"}`, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestNotify_ForeignProvider403(t *testing.T) {
	mux, _ := testMux(t, &notifyFakeStarter{}, policy.NewMemoryLimiter(nil))
	rec := post(t, mux, "/v1/notify/email", "bk_k1_secret123",
		`{"to":"a@b.com","subject":"s","provider":"mailchimp"}`, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNotify_RateLimited429(t *testing.T) {
	now := time.Now()
	limiter := policy.NewMemoryLimiter(func() time.Time { return now })
	mux, _ := testMux(t, &notifyFakeStarter{}, limiter)
	for i := 0; i < 60; i++ {
		post(t, mux, "/v1/notify/email", "bk_k1_secret123", goodBody, nil)
	}
	rec := post(t, mux, "/v1/notify/email", "bk_k1_secret123", goodBody, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header required on 429")
	}
}

func TestNotify_IdempotencyKeySetsWorkflowID(t *testing.T) {
	starter := &notifyFakeStarter{}
	mux, _ := testMux(t, starter, policy.NewMemoryLimiter(nil))
	rec := post(t, mux, "/v1/notify/email", "bk_k1_secret123", goodBody,
		map[string]string{"Idempotency-Key": "receipt-8812"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d", rec.Code)
	}
	if starter.lastOptions.ID != "email:billing-api:receipt-8812" {
		t.Fatalf("workflow ID: %q", starter.lastOptions.ID)
	}
}

func TestNotify_BadIdempotencyKey400(t *testing.T) {
	mux, _ := testMux(t, &notifyFakeStarter{}, policy.NewMemoryLimiter(nil))
	rec := post(t, mux, "/v1/notify/email", "bk_k1_secret123", goodBody,
		map[string]string{"Idempotency-Key": "has spaces!"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestNotify_OversizedBody413(t *testing.T) {
	mux, _ := testMux(t, &notifyFakeStarter{}, policy.NewMemoryLimiter(nil))
	big := bytes.Repeat([]byte("x"), 257<<10)
	body := `{"to":"a@b.com","subject":"s","body":"` + string(big) + `"}`
	rec := post(t, mux, "/v1/notify/email", "bk_k1_secret123", body, nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d", rec.Code)
	}
}

func TestNotify_AdminTokenForbidden(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "operator-secret")
	mux, _ := testMux(t, &notifyFakeStarter{}, policy.NewMemoryLimiter(nil))
	rec := post(t, mux, "/v1/notify/email", "operator-secret", goodBody, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNotify_ChannelNotEnabledForService403(t *testing.T) {
	mux, _ := testMux(t, &notifyFakeStarter{}, policy.NewMemoryLimiter(nil))
	rec := post(t, mux, "/v1/notify/email", "bk_m1_metricsecret", goodBody, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not enabled for service") {
		t.Fatalf("expected body to mention 'not enabled for service', got: %s", rec.Body.String())
	}
}

func TestNotify_ProviderAllowedButNotConfigured503(t *testing.T) {
	mux, _ := testMux(t, &notifyFakeStarter{}, policy.NewMemoryLimiter(nil))
	rec := post(t, mux, "/v1/notify/email", "bk_k1_secret123",
		`{"to":"a@b.com","subject":"s","provider":"ses"}`, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Fatalf("expected body to mention 'not configured', got: %s", rec.Body.String())
	}
}

func TestNotify_TemporalError500(t *testing.T) {
	starter := &notifyFakeStarter{err: errors.New("boom")}
	mux, _ := testMux(t, starter, policy.NewMemoryLimiter(nil))
	rec := post(t, mux, "/v1/notify/email", "bk_k1_secret123", goodBody, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNotify_DuplicateStart202(t *testing.T) {
	starter := &notifyFakeStarter{err: serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "", "")}
	mux, _ := testMux(t, starter, policy.NewMemoryLimiter(nil))
	rec := post(t, mux, "/v1/notify/email", "bk_k1_secret123", goodBody, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data["duplicate"] != true {
		t.Fatalf("expected duplicate=true, got: %+v", resp.Data)
	}
	if resp.Data["workflow_run_id"] != "" {
		t.Fatalf("expected empty workflow_run_id for duplicate, got: %+v", resp.Data)
	}
}

func TestNotify_NilTemporal503(t *testing.T) {
	mux, _ := buildNotifyMux(t, testBundle(), nil, policy.NewMemoryLimiter(nil))
	rec := post(t, mux, "/v1/notify/email", "bk_k1_secret123", goodBody, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", rec.Code, rec.Body.String())
	}
}
