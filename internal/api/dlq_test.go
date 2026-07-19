package api

import (
	"beacon/internal/auth"
	"beacon/internal/dlq"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDLQ is a test double for DLQQuerier.
type fakeDLQ struct {
	failures        []*dlq.FailedNotification
	qErr            error
	replay          *dlq.ReplayResult
	rErr            error
	gotFilter       dlq.FailureFilter
	gotWorkflowID   string
	gotCallerTenant string
}

func (f *fakeDLQ) QueryFailures(_ context.Context, flt dlq.FailureFilter) ([]*dlq.FailedNotification, error) {
	f.gotFilter = flt
	return f.failures, f.qErr
}

func (f *fakeDLQ) ReplayWorkflow(_ context.Context, workflowID, callerTenant string) (*dlq.ReplayResult, error) {
	f.gotWorkflowID = workflowID
	f.gotCallerTenant = callerTenant
	return f.replay, f.rErr
}

// discardLogger returns a slog.Logger that writes nothing.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// buildDLQMux wires DLQHandler behind auth.Middleware on the production v1
// route patterns (method-scoped, per internal/app/server.go), using the
// shared testBundle() fixture from notify_test.go: "billing-api" (tenant
// "payments", key bk_k1_secret123) and "metrics-agent" (tenant "obs", key
// bk_m1_metricsecret).
func buildDLQMux(t *testing.T, fake *fakeDLQ) http.Handler {
	t.Helper()
	reg := auth.NewRegistry(testBundle())
	h := NewDLQHandler(fake, discardLogger())
	authMW := auth.Middleware(reg)
	mux := http.NewServeMux()
	mux.Handle("GET /v1/dlq/failed", authMW(http.HandlerFunc(h.HandleQueryFailures)))
	mux.Handle("POST /v1/dlq/replay/{workflowID}", authMW(http.HandlerFunc(h.HandleReplay)))
	return mux
}

func withBearer(req *http.Request, key string) *http.Request {
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return req
}

const billingAPIKey = "bk_k1_secret123"      // service "billing-api", tenant "payments"
const metricsAgentKey = "bk_m1_metricsecret" // service "metrics-agent", tenant "obs"

// ---- HandleQueryFailures tests ----

func TestDLQ_HandleQueryFailures_MethodNotAllowed(t *testing.T) {
	mux := buildDLQMux(t, &fakeDLQ{})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/v1/dlq/failed", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, "method %s should return 405", method)
	}
}

func TestDLQ_HandleQueryFailures_Unauthenticated401(t *testing.T) {
	mux := buildDLQMux(t, &fakeDLQ{})
	req := httptest.NewRequest(http.MethodGet, "/v1/dlq/failed", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestDLQ_HandleQueryFailures_BadFromDate(t *testing.T) {
	mux := buildDLQMux(t, &fakeDLQ{})
	req := withBearer(httptest.NewRequest(http.MethodGet, "/v1/dlq/failed?from=not-a-date", nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDLQ_HandleQueryFailures_BadToDate(t *testing.T) {
	mux := buildDLQMux(t, &fakeDLQ{})
	req := withBearer(httptest.NewRequest(http.MethodGet, "/v1/dlq/failed?to=not-a-date", nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDLQ_HandleQueryFailures_LimitPassedThrough(t *testing.T) {
	fake := &fakeDLQ{failures: []*dlq.FailedNotification{}}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodGet, "/v1/dlq/failed?limit=999", nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	// Clamping is owned by the dlq service (QueryFailures caps limit at 100);
	// the handler passes the raw value through.
	assert.Equal(t, 999, fake.gotFilter.Limit)
}

func TestDLQ_HandleQueryFailures_QueryError(t *testing.T) {
	fake := &fakeDLQ{qErr: io.ErrUnexpectedEOF}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodGet, "/v1/dlq/failed", nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestDLQ_HandleQueryFailures_Success(t *testing.T) {
	items := []*dlq.FailedNotification{
		{WorkflowID: "wf-abc", RunID: "run-1", Status: "Failed"},
		{WorkflowID: "wf-def", RunID: "run-2", Status: "TimedOut"},
	}
	fake := &fakeDLQ{failures: items}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodGet, "/v1/dlq/failed", nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))

	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "response body should have a 'data' map")
	assert.Equal(t, float64(2), data["count"])
}

func TestDLQ_HandleQueryFailures_FiltersPassedThrough(t *testing.T) {
	fake := &fakeDLQ{failures: []*dlq.FailedNotification{}}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodGet,
		"/v1/dlq/failed?status=Failed&provider=sendgrid&limit=5&offset=10&from=2024-01-01T00:00:00Z&to=2024-12-31T23:59:59Z",
		nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Failed", fake.gotFilter.Status)
	assert.Equal(t, "sendgrid", fake.gotFilter.Provider)
	assert.Equal(t, 5, fake.gotFilter.Limit)
	assert.Equal(t, 10, fake.gotFilter.Offset)
	assert.False(t, fake.gotFilter.FromDate.IsZero())
	assert.False(t, fake.gotFilter.ToDate.IsZero())
}

// ---- tenant scoping (Task 10) ----

func TestDLQ_HandleQueryFailures_NonAdminHardScopedToOwnTenant(t *testing.T) {
	fake := &fakeDLQ{failures: []*dlq.FailedNotification{}}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodGet, "/v1/dlq/failed", nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "payments", fake.gotFilter.Tenant)
}

func TestDLQ_HandleQueryFailures_NonAdminCannotOverrideTenantViaQueryParam(t *testing.T) {
	fake := &fakeDLQ{failures: []*dlq.FailedNotification{}}
	mux := buildDLQMux(t, fake)

	// billing-api attempts to widen its view via ?tenant=; must be ignored.
	req := withBearer(httptest.NewRequest(http.MethodGet, "/v1/dlq/failed?tenant=obs", nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "payments", fake.gotFilter.Tenant)
}

func TestDLQ_HandleQueryFailures_AdminUnscopedByDefault(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "operator-secret")
	fake := &fakeDLQ{failures: []*dlq.FailedNotification{}}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodGet, "/v1/dlq/failed", nil), "operator-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "", fake.gotFilter.Tenant)
}

func TestDLQ_HandleQueryFailures_AdminMayNarrowByTenantParam(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "operator-secret")
	fake := &fakeDLQ{failures: []*dlq.FailedNotification{}}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodGet, "/v1/dlq/failed?tenant=payments", nil), "operator-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "payments", fake.gotFilter.Tenant)
}

// ---- HandleReplay tests ----

func TestDLQ_HandleReplay_MethodNotAllowed(t *testing.T) {
	mux := buildDLQMux(t, &fakeDLQ{})

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/v1/dlq/replay/wf-123", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, "method %s should return 405", method)
	}
}

func TestDLQ_HandleReplay_Unauthenticated401(t *testing.T) {
	mux := buildDLQMux(t, &fakeDLQ{})
	req := httptest.NewRequest(http.MethodPost, "/v1/dlq/replay/wf-123", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestDLQ_HandleReplay_EmptyWorkflowID(t *testing.T) {
	// Invoke the auth-wrapped handler directly (bypassing the mux's
	// {workflowID} pattern) so req.PathValue("workflowID") is unpopulated,
	// exercising the handler's own defensive empty-ID guard.
	reg := auth.NewRegistry(testBundle())
	h := NewDLQHandler(&fakeDLQ{}, discardLogger())
	handler := auth.Middleware(reg)(http.HandlerFunc(h.HandleReplay))

	req := withBearer(httptest.NewRequest(http.MethodPost, "/v1/dlq/replay/", nil), billingAPIKey)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDLQ_HandleReplay_WorkflowNotFound(t *testing.T) {
	fake := &fakeDLQ{rErr: dlq.ErrWorkflowNotFound}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodPost, "/v1/dlq/replay/missing-wf", nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDLQ_HandleReplay_NotTerminalState(t *testing.T) {
	fake := &fakeDLQ{rErr: dlq.ErrNotTerminalState}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodPost, "/v1/dlq/replay/running-wf", nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestDLQ_HandleReplay_AlreadyRunning(t *testing.T) {
	fake := &fakeDLQ{rErr: dlq.ErrReplayAlreadyRunning}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodPost, "/v1/dlq/replay/active-wf", nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestDLQ_HandleReplay_OtherError(t *testing.T) {
	fake := &fakeDLQ{rErr: io.ErrUnexpectedEOF}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodPost, "/v1/dlq/replay/bad-wf", nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestDLQ_HandleReplay_Success(t *testing.T) {
	result := &dlq.ReplayResult{
		NewWorkflowID:      "replay-wf-original",
		NewRunID:           "run-new",
		OriginalWorkflowID: "wf-original",
		Provider:           "sendgrid",
	}
	fake := &fakeDLQ{replay: result}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodPost, "/v1/dlq/replay/wf-original", nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.True(t, body["success"].(bool))
	assert.Equal(t, "wf-original", fake.gotWorkflowID)
}

// ---- tenant scoping for replay (Task 10) ----

func TestDLQ_HandleReplay_NonAdminCallerTenantScoped(t *testing.T) {
	fake := &fakeDLQ{replay: &dlq.ReplayResult{NewWorkflowID: "replay-wf-1"}}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodPost, "/v1/dlq/replay/wf-1", nil), billingAPIKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "payments", fake.gotCallerTenant)
}

func TestDLQ_HandleReplay_MetricsAgentCallerTenantScoped(t *testing.T) {
	fake := &fakeDLQ{replay: &dlq.ReplayResult{NewWorkflowID: "replay-wf-2"}}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodPost, "/v1/dlq/replay/wf-2", nil), metricsAgentKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "obs", fake.gotCallerTenant)
}

func TestDLQ_HandleReplay_AdminCallerTenantUnscoped(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "operator-secret")
	fake := &fakeDLQ{replay: &dlq.ReplayResult{NewWorkflowID: "replay-wf-3"}}
	mux := buildDLQMux(t, fake)

	req := withBearer(httptest.NewRequest(http.MethodPost, "/v1/dlq/replay/wf-3", nil), "operator-secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "", fake.gotCallerTenant)
}
