package api

import (
	"beacon/internal/config"
	"beacon/internal/notifier"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
)

// fakeStarter is a test double for WorkflowStarter.
type fakeStarter struct {
	run    client.WorkflowRun
	err    error
	called bool
}

func (f *fakeStarter) ExecuteWorkflow(_ context.Context, _ client.StartWorkflowOptions, _ interface{}, _ ...interface{}) (client.WorkflowRun, error) {
	f.called = true
	return f.run, f.err
}

// fakeRun is a test double for client.WorkflowRun.
type fakeRun struct{}

func (fakeRun) GetID() string                              { return "wf-1" }
func (fakeRun) GetRunID() string                           { return "run-1" }
func (fakeRun) Get(_ context.Context, _ interface{}) error { return nil }
func (fakeRun) GetWithOptions(_ context.Context, _ interface{}, _ client.WorkflowRunGetOptions) error {
	return nil
}

// minimalBundle returns a ConfigBundle with one default SMTP provider for registry construction.
func minimalBundle() *config.ConfigBundle {
	return &config.ConfigBundle{
		Revision: 1,
		SMTP: map[string]*config.SMTPClientConfig{
			"testprovider": {
				Name:        "testprovider",
				Provider:    "smtp",
				Host:        "smtp.example.com",
				Port:        587,
				Username:    "user",
				Password:    "pass",
				AuthType:    config.AuthPlain,
				FromAddress: "noreply@example.com",
				IsDefault:   true,
			},
		},
	}
}

// multiProviderBundle returns a bundle with two providers and no auto-default.
// The "marketing" category maps to the "secondary" provider; the primary has no default flag,
// so when an unknown hint is given the registry returns an error.
func multiProviderBundle() *config.ConfigBundle {
	return &config.ConfigBundle{
		Revision: 1,
		SMTP: map[string]*config.SMTPClientConfig{
			"primary": {
				Name:        "primary",
				Provider:    "smtp",
				Host:        "smtp.primary.com",
				Port:        587,
				Username:    "user",
				Password:    "pass",
				AuthType:    config.AuthPlain,
				FromAddress: "a@example.com",
				// IsDefault is false and no categories — so no default is set
			},
			"secondary": {
				Name:        "secondary",
				Provider:    "smtp",
				Host:        "smtp.secondary.com",
				Port:        587,
				Username:    "user2",
				Password:    "pass2",
				AuthType:    config.AuthPlain,
				FromAddress: "b@example.com",
				Categories:  []string{"marketing"},
			},
		},
	}
}

// buildRegistry is a helper that panics if registry construction fails (test setup error).
func buildRegistry(t *testing.T, bundle *config.ConfigBundle) *notifier.EmailClientRegistry {
	t.Helper()
	r, err := notifier.NewEmailClientRegistry(bundle)
	require.NoError(t, err, "registry construction failed")
	return r
}

func validEmailBody(t *testing.T, to, subject string) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(map[string]string{"to": to, "subject": subject})
	require.NoError(t, err)
	return bytes.NewBuffer(b)
}

// ---- HandleRequest tests ----

func TestEmail_HandleRequest_MethodNotAllowed(t *testing.T) {
	reg := buildRegistry(t, minimalBundle())
	h := &EmailHandler{TemporalClient: &fakeStarter{run: fakeRun{}}, Registry: reg}

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/notify/email", nil)
		w := httptest.NewRecorder()
		h.HandleRequest(w, req)
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, "method %s should be 405", method)
	}
}

func TestEmail_HandleRequest_NilTemporalClient(t *testing.T) {
	reg := buildRegistry(t, minimalBundle())

	// Explicitly a nil interface value — matches the handler's `h.TemporalClient == nil` check.
	h := &EmailHandler{TemporalClient: nil, Registry: reg}

	req := httptest.NewRequest(http.MethodPost, "/notify/email", validEmailBody(t, "user@example.com", "Hello"))
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestEmail_HandleRequest_BadJSONBody(t *testing.T) {
	reg := buildRegistry(t, minimalBundle())
	h := &EmailHandler{TemporalClient: &fakeStarter{run: fakeRun{}}, Registry: reg}

	req := httptest.NewRequest(http.MethodPost, "/notify/email", strings.NewReader("not-json"))
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEmail_HandleRequest_MissingTo(t *testing.T) {
	reg := buildRegistry(t, minimalBundle())
	h := &EmailHandler{TemporalClient: &fakeStarter{run: fakeRun{}}, Registry: reg}

	body, _ := json.Marshal(map[string]string{"subject": "Hello"})
	req := httptest.NewRequest(http.MethodPost, "/notify/email", bytes.NewBuffer(body))
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEmail_HandleRequest_MissingSubject(t *testing.T) {
	reg := buildRegistry(t, minimalBundle())
	h := &EmailHandler{TemporalClient: &fakeStarter{run: fakeRun{}}, Registry: reg}

	body, _ := json.Marshal(map[string]string{"to": "user@example.com"})
	req := httptest.NewRequest(http.MethodPost, "/notify/email", bytes.NewBuffer(body))
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEmail_HandleRequest_InvalidEmailAddress(t *testing.T) {
	reg := buildRegistry(t, minimalBundle())
	h := &EmailHandler{TemporalClient: &fakeStarter{run: fakeRun{}}, Registry: reg}

	req := httptest.NewRequest(http.MethodPost, "/notify/email", validEmailBody(t, "not-an-email", "Hello"))
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEmail_HandleRequest_RoutingError_UnknownHint(t *testing.T) {
	// Multi-provider bundle: no default provider configured, and hint "unknown" maps to nothing.
	reg := buildRegistry(t, multiProviderBundle())
	h := &EmailHandler{TemporalClient: &fakeStarter{run: fakeRun{}}, Registry: reg}

	body, _ := json.Marshal(map[string]string{
		"to":          "user@example.com",
		"subject":     "Hello",
		"client_hint": "unknown-hint",
	})
	req := httptest.NewRequest(http.MethodPost, "/notify/email", bytes.NewBuffer(body))
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEmail_HandleRequest_ExecuteWorkflowError(t *testing.T) {
	reg := buildRegistry(t, minimalBundle())
	starter := &fakeStarter{err: errors.New("temporal connection refused")}
	h := &EmailHandler{TemporalClient: starter, Registry: reg}

	req := httptest.NewRequest(http.MethodPost, "/notify/email", validEmailBody(t, "user@example.com", "Hello"))
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.True(t, starter.called, "ExecuteWorkflow should have been called")
}

func TestEmail_HandleRequest_Success(t *testing.T) {
	reg := buildRegistry(t, minimalBundle())
	starter := &fakeStarter{run: fakeRun{}}
	h := &EmailHandler{TemporalClient: starter, Registry: reg}

	req := httptest.NewRequest(http.MethodPost, "/notify/email", validEmailBody(t, "user@example.com", "Hello"))
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)
	assert.True(t, starter.called)

	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.True(t, body["success"].(bool))

	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "wf-1", data["workflow_id"])
	assert.Equal(t, "run-1", data["workflow_run_id"])
	assert.Equal(t, "testprovider", data["provider"])
}

func TestEmail_HandleRequest_Success_WithMatchingHint(t *testing.T) {
	reg := buildRegistry(t, multiProviderBundle())
	starter := &fakeStarter{run: fakeRun{}}
	h := &EmailHandler{TemporalClient: starter, Registry: reg}

	body, _ := json.Marshal(map[string]string{
		"to":          "user@example.com",
		"subject":     "Marketing mail",
		"client_hint": "marketing",
	})
	req := httptest.NewRequest(http.MethodPost, "/notify/email", bytes.NewBuffer(body))
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)

	require.Equal(t, http.StatusAccepted, w.Code)
	data := body // reuse slice

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	_ = data
	assert.True(t, resp["success"].(bool))
	assert.Equal(t, "secondary", resp["data"].(map[string]any)["provider"])
}

func TestEmail_HandleRequest_ToWithWhitespace(t *testing.T) {
	reg := buildRegistry(t, minimalBundle())
	starter := &fakeStarter{run: fakeRun{}}
	h := &EmailHandler{TemporalClient: starter, Registry: reg}

	// "to" field with surrounding spaces should be trimmed and accepted
	body, _ := json.Marshal(map[string]string{
		"to":      "  user@example.com  ",
		"subject": "Hello",
	})
	req := httptest.NewRequest(http.MethodPost, "/notify/email", bytes.NewBuffer(body))
	w := httptest.NewRecorder()
	h.HandleRequest(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
}
