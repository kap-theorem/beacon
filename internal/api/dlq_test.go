package api

import (
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
	failures  []*dlq.FailedNotification
	qErr      error
	replay    *dlq.ReplayResult
	rErr      error
	gotFilter dlq.FailureFilter
}

func (f *fakeDLQ) QueryFailures(_ context.Context, flt dlq.FailureFilter) ([]*dlq.FailedNotification, error) {
	f.gotFilter = flt
	return f.failures, f.qErr
}

func (f *fakeDLQ) ReplayWorkflow(_ context.Context, _ string) (*dlq.ReplayResult, error) {
	return f.replay, f.rErr
}

// discardLogger returns a slog.Logger that writes nothing.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---- HandleQueryFailures tests ----

func TestDLQ_HandleQueryFailures_MethodNotAllowed(t *testing.T) {
	h := NewDLQHandler(&fakeDLQ{}, discardLogger())

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/dlq/failed", nil)
		w := httptest.NewRecorder()
		h.HandleQueryFailures(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, "method %s should return 405", method)
	}
}

func TestDLQ_HandleQueryFailures_BadFromDate(t *testing.T) {
	h := NewDLQHandler(&fakeDLQ{}, discardLogger())
	req := httptest.NewRequest(http.MethodGet, "/dlq/failed?from=not-a-date", nil)
	w := httptest.NewRecorder()
	h.HandleQueryFailures(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDLQ_HandleQueryFailures_BadToDate(t *testing.T) {
	h := NewDLQHandler(&fakeDLQ{}, discardLogger())
	req := httptest.NewRequest(http.MethodGet, "/dlq/failed?to=not-a-date", nil)
	w := httptest.NewRecorder()
	h.HandleQueryFailures(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDLQ_HandleQueryFailures_LimitPassedThrough(t *testing.T) {
	fake := &fakeDLQ{failures: []*dlq.FailedNotification{}}
	h := NewDLQHandler(fake, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/dlq/failed?limit=999", nil)
	w := httptest.NewRecorder()
	h.HandleQueryFailures(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	// Clamping is owned by the dlq service (QueryFailures caps limit at 100);
	// the handler passes the raw value through.
	assert.Equal(t, 999, fake.gotFilter.Limit)
}

func TestDLQ_HandleQueryFailures_QueryError(t *testing.T) {
	fake := &fakeDLQ{qErr: io.ErrUnexpectedEOF}
	h := NewDLQHandler(fake, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/dlq/failed", nil)
	w := httptest.NewRecorder()
	h.HandleQueryFailures(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestDLQ_HandleQueryFailures_Success(t *testing.T) {
	items := []*dlq.FailedNotification{
		{WorkflowID: "wf-abc", RunID: "run-1", Status: "Failed"},
		{WorkflowID: "wf-def", RunID: "run-2", Status: "TimedOut"},
	}
	fake := &fakeDLQ{failures: items}
	h := NewDLQHandler(fake, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/dlq/failed", nil)
	w := httptest.NewRecorder()
	h.HandleQueryFailures(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))

	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "response body should have a 'data' map")
	assert.Equal(t, float64(2), data["count"])
}

func TestDLQ_HandleQueryFailures_FiltersPassedThrough(t *testing.T) {
	fake := &fakeDLQ{failures: []*dlq.FailedNotification{}}
	h := NewDLQHandler(fake, discardLogger())

	req := httptest.NewRequest(http.MethodGet,
		"/dlq/failed?status=Failed&provider=sendgrid&limit=5&offset=10&from=2024-01-01T00:00:00Z&to=2024-12-31T23:59:59Z",
		nil)
	w := httptest.NewRecorder()
	h.HandleQueryFailures(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Failed", fake.gotFilter.Status)
	assert.Equal(t, "sendgrid", fake.gotFilter.Provider)
	assert.Equal(t, 5, fake.gotFilter.Limit)
	assert.Equal(t, 10, fake.gotFilter.Offset)
	assert.False(t, fake.gotFilter.FromDate.IsZero())
	assert.False(t, fake.gotFilter.ToDate.IsZero())
}

// ---- HandleReplay tests ----

func TestDLQ_HandleReplay_MethodNotAllowed(t *testing.T) {
	h := NewDLQHandler(&fakeDLQ{}, discardLogger())

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/dlq/replay/wf-123", nil)
		w := httptest.NewRecorder()
		h.HandleReplay(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, "method %s should return 405", method)
	}
}

func TestDLQ_HandleReplay_EmptyWorkflowID(t *testing.T) {
	h := NewDLQHandler(&fakeDLQ{}, discardLogger())
	// The path ends with /dlq/replay/ so TrimPrefix leaves an empty string.
	req := httptest.NewRequest(http.MethodPost, "/dlq/replay/", nil)
	w := httptest.NewRecorder()
	h.HandleReplay(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDLQ_HandleReplay_WorkflowNotFound(t *testing.T) {
	fake := &fakeDLQ{rErr: dlq.ErrWorkflowNotFound}
	h := NewDLQHandler(fake, discardLogger())

	req := httptest.NewRequest(http.MethodPost, "/dlq/replay/missing-wf", nil)
	w := httptest.NewRecorder()
	h.HandleReplay(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDLQ_HandleReplay_NotTerminalState(t *testing.T) {
	fake := &fakeDLQ{rErr: dlq.ErrNotTerminalState}
	h := NewDLQHandler(fake, discardLogger())

	req := httptest.NewRequest(http.MethodPost, "/dlq/replay/running-wf", nil)
	w := httptest.NewRecorder()
	h.HandleReplay(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestDLQ_HandleReplay_AlreadyRunning(t *testing.T) {
	fake := &fakeDLQ{rErr: dlq.ErrReplayAlreadyRunning}
	h := NewDLQHandler(fake, discardLogger())

	req := httptest.NewRequest(http.MethodPost, "/dlq/replay/active-wf", nil)
	w := httptest.NewRecorder()
	h.HandleReplay(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestDLQ_HandleReplay_OtherError(t *testing.T) {
	fake := &fakeDLQ{rErr: io.ErrUnexpectedEOF}
	h := NewDLQHandler(fake, discardLogger())

	req := httptest.NewRequest(http.MethodPost, "/dlq/replay/bad-wf", nil)
	w := httptest.NewRecorder()
	h.HandleReplay(w, req)
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
	h := NewDLQHandler(fake, discardLogger())

	req := httptest.NewRequest(http.MethodPost, "/dlq/replay/wf-original", nil)
	w := httptest.NewRecorder()
	h.HandleReplay(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.True(t, body["success"].(bool))
}
