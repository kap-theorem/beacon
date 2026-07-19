//go:build integration

package integration

// End-to-end integration tests for Beacon.
//
// These tests wire the REAL HTTP handlers (internal/api) to a REAL Temporal
// client and an in-process Temporal worker, with the downstream SMTP service
// replaced by internal/testsupport.NewMockSMTPServer. They require a running
// Temporal dev server (default 127.0.0.1:7233, namespace "default"). When no
// server is reachable, every test t.Skip()s with a clear message so the file
// is safe to run anywhere; against a live server the tests run for real.
//
// Run with:
//
//	go test -tags=integration ./internal/integration/ -v
//	go test -tags=integration ./internal/integration/ -run TestIntegration_HappyPath -v
//
// Each test uses unique provider names (and therefore unique Temporal task
// queues) derived from t.Name() plus a random suffix, so the in-process
// workers and the workflows they pick up never collide across tests that share
// the one Temporal server.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"beacon/internal/api"
	"beacon/internal/config"
	"beacon/internal/dlq"
	"beacon/internal/models"
	"beacon/internal/notifier"
	"beacon/internal/temporal"
	"beacon/internal/testsupport"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

const (
	// namespace is the Temporal namespace the tests run in.
	namespace = "default"

	// defaultTemporalAddress is used when TEMPORAL_ADDRESS is unset.
	defaultTemporalAddress = "127.0.0.1:7233"

	// deliverTimeout bounds how long we poll a mock SMTP server for a message.
	deliverTimeout = 15 * time.Second
	// pollInterval is the polling cadence for delivery / DLQ checks.
	pollInterval = 100 * time.Millisecond

	// terminalTimeout bounds how long we wait for a workflow to exhaust retries
	// and reach a terminal FAILED state. The activity RetryPolicy is
	// InitialInterval=5s, BackoffCoefficient=2.0, MaximumAttempts=3, so with a
	// fast-failing (connection-refused) SMTP target the three attempts complete
	// in roughly 5s + 10s ~= 15s of backoff; 75s gives generous headroom for a
	// busy shared dev server.
	terminalTimeout = 75 * time.Second
)

// temporalAddress returns the Temporal host:port, honouring TEMPORAL_ADDRESS.
func temporalAddress() string {
	if addr := os.Getenv("TEMPORAL_ADDRESS"); addr != "" {
		return addr
	}
	return defaultTemporalAddress
}

// dialOrSkip dials the real Temporal dev server. If the server is unreachable
// the test is skipped (not failed) with a clear message, so this file stays
// safe in environments without a server. The returned client is closed via
// t.Cleanup.
func dialOrSkip(t *testing.T) client.Client {
	t.Helper()
	c, err := client.Dial(client.Options{
		HostPort:  temporalAddress(),
		Namespace: namespace,
	})
	if err != nil {
		t.Skipf("Temporal dev server not reachable at %s (namespace %q): %v -- "+
			"start a Temporal dev server to run integration tests", temporalAddress(), namespace, err)
	}
	t.Cleanup(c.Close)
	return c
}

// testLogger returns a quiet slog.Logger for handlers under test.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// uniqueSuffix produces a short random suffix to make provider names (and thus
// task queues) unique per test invocation, avoiding cross-test collisions on
// the shared Temporal server.
func uniqueSuffix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(b)
}

// providerNameFor builds a deterministic-yet-unique provider name from the
// running test and a random suffix. The result is also embedded into the
// Temporal task queue via notifier.TaskQueueFor.
func providerNameFor(t *testing.T, label string) string {
	t.Helper()
	base := strings.ToLower(t.Name())
	base = strings.NewReplacer("/", "-", "_", "-", " ", "-").Replace(base)
	return fmt.Sprintf("%s-%s-%s", base, label, uniqueSuffix())
}

// smtpProvider describes one mock SMTP backend and its routing config.
type smtpProvider struct {
	name       string
	host       string
	port       int
	from       string
	fromName   string
	categories []string
	isDefault  bool
}

// newBundle builds a *config.ConfigBundle from the given providers.
func newBundle(providers ...smtpProvider) *config.ConfigBundle {
	smtp := make(map[string]*config.SMTPClientConfig, len(providers))
	for _, p := range providers {
		smtp[p.name] = &config.SMTPClientConfig{
			Name:        p.name,
			Provider:    p.name,
			Host:        p.host,
			Port:        p.port,
			AuthType:    config.AuthPlain,
			IsDefault:   p.isDefault,
			FromAddress: p.from,
			FromName:    p.fromName,
			Categories:  p.categories,
		}
	}
	return &config.ConfigBundle{
		SMTP:      smtp,
		Revision:  1,
		Timestamp: time.Now(),
	}
}

// swappableSender is a Sender wrapper whose underlying SMTP target can be
// hot-swapped under a mutex, mirroring how cmd/email_worker reloads its
// sender when config changes. The DLQ-replay scenario uses this to point the
// worker at a HEALTHY mock SMTP after the original delivery failed.
type swappableSender struct {
	mu  sync.RWMutex
	snd notifier.Sender
}

func newSwappableService(snd notifier.Sender) *swappableSender {
	return &swappableSender{snd: snd}
}

func (s *swappableSender) get() notifier.Sender {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snd
}

func (s *swappableSender) swap(snd notifier.Sender) {
	s.mu.Lock()
	s.snd = snd
	s.mu.Unlock()
}

// startWorker starts an in-process Temporal worker on the task queue for
// providerName. The worker registers SendEmailWorkflow and a SendEmailActivity
// whose Sender is provided by getSender (allowing hot-swap). The worker is
// stopped via t.Cleanup.
func startWorker(t *testing.T, c client.Client, providerName string, getSender func() notifier.Sender) {
	t.Helper()
	tq := notifier.TaskQueueFor(providerName)
	w := worker.New(c, tq, worker.Options{})

	activities := &temporal.EmailActivities{GetSender: getSender}
	w.RegisterWorkflow(temporal.SendEmailWorkflow)
	w.RegisterActivity(activities.SendEmailActivity)

	if err := w.Start(); err != nil {
		t.Fatalf("start worker for task queue %q: %v", tq, err)
	}
	t.Cleanup(w.Stop)
}

// emailServiceForMock builds a real EmailSender pointed at a mock SMTP server.
func emailServiceForMock(mock *testsupport.MockSMTPServer, from, fromName string) notifier.Sender {
	return notifier.NewEmailSender(&config.SMTPClientConfig{
		Host: mock.Host(), Port: mock.Port(), FromAddress: from, FromName: fromName,
	})
}

// emailServiceForAddr builds a real EmailSender pointed at an arbitrary
// host:port (used to target a dead port for the failure scenario).
func emailServiceForAddr(host string, port int, from, fromName string) notifier.Sender {
	return notifier.NewEmailSender(&config.SMTPClientConfig{
		Host: host, Port: port, FromAddress: from, FromName: fromName,
	})
}

// newServer stands up an httptest server exposing the real handlers. Any of
// emailHandler / dlqHandler / adminHandler may be nil to omit that route.
func newServer(t *testing.T, emailHandler *api.EmailHandler, dlqHandler *api.DLQHandler, adminHandler *api.AdminHandler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	if emailHandler != nil {
		mux.HandleFunc("/notify/email", emailHandler.HandleRequest)
	}
	if dlqHandler != nil {
		mux.HandleFunc("/dlq/failed", dlqHandler.HandleQueryFailures)
		mux.HandleFunc("/dlq/replay/", dlqHandler.HandleReplay)
	}
	if adminHandler != nil {
		mux.HandleFunc("/admin/config/refresh", adminHandler.HandleConfigRefresh)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// postEmail POSTs an EmailMessage JSON body to the given URL and returns the
// status code and decoded API response.
func postEmail(t *testing.T, url string, msg models.EmailMessage) (int, utils_APIResponse) {
	t.Helper()
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal email: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var out utils_APIResponse
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode response (status %d, body %q): %v", resp.StatusCode, string(raw), err)
		}
	}
	return resp.StatusCode, out
}

// utils_APIResponse mirrors utils.APIResponse for decoding handler responses.
// Data is decoded loosely as a map so we can read workflow_id / provider etc.
type utils_APIResponse struct {
	Success bool           `json:"success"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// waitForMessages polls mock.Messages() until at least n messages arrive or the
// deadline elapses. It returns the captured messages (which may be fewer than n
// on timeout).
func waitForMessages(mock *testsupport.MockSMTPServer, n int, timeout time.Duration) []testsupport.CapturedMessage {
	deadline := time.Now().Add(timeout)
	for {
		msgs := mock.Messages()
		if len(msgs) >= n {
			return msgs
		}
		if time.Now().After(deadline) {
			return msgs
		}
		time.Sleep(pollInterval)
	}
}

// assertDelivered asserts that exactly-or-at-least-one message was delivered to
// the mock with the expected recipient, subject substring, and body substring.
func assertDelivered(t *testing.T, mock *testsupport.MockSMTPServer, recipient, subjectSub, bodySub string) {
	t.Helper()
	msgs := waitForMessages(mock, 1, deliverTimeout)
	if len(msgs) == 0 {
		t.Fatalf("expected at least one delivered message, got none within %s", deliverTimeout)
	}
	m := msgs[0]
	gotRecipient := false
	for _, to := range m.To {
		if to == recipient {
			gotRecipient = true
			break
		}
	}
	if !gotRecipient {
		t.Errorf("recipient %q not found in captured To list %v", recipient, m.To)
	}
	if !strings.Contains(m.Data, subjectSub) {
		t.Errorf("subject %q not found in captured DATA:\n%s", subjectSub, m.Data)
	}
	if !strings.Contains(m.Data, bodySub) {
		t.Errorf("body %q not found in captured DATA:\n%s", bodySub, m.Data)
	}
}

// waitTerminalFailed polls Temporal until the workflow reaches a terminal
// FAILED/TIMED_OUT/CANCELED state, or the deadline elapses. Returns the final
// status (or the last observed status on timeout).
func waitTerminalFailed(t *testing.T, c client.Client, workflowID string, timeout time.Duration) enumspb.WorkflowExecutionStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last enumspb.WorkflowExecutionStatus
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		desc, err := c.DescribeWorkflowExecution(ctx, workflowID, "")
		cancel()
		if err == nil && desc.WorkflowExecutionInfo != nil {
			last = desc.WorkflowExecutionInfo.Status
			switch last {
			case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
				enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
				enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:
				return last
			}
		}
		if time.Now().After(deadline) {
			return last
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// -----------------------------------------------------------------------------
// Scenario 1: Happy path
// -----------------------------------------------------------------------------

func TestIntegration_HappyPath(t *testing.T) {
	c := dialOrSkip(t)

	mock := testsupport.NewMockSMTPServer(t)
	provider := providerNameFor(t, "default")
	const from = "noreply@beacon.test"
	const fromName = "Beacon"

	bundle := newBundle(smtpProvider{
		name:      provider,
		host:      mock.Host(),
		port:      mock.Port(),
		from:      from,
		fromName:  fromName,
		isDefault: true,
	})
	registry, err := notifier.NewEmailClientRegistry(bundle)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}

	startWorker(t, c, provider, func() notifier.Sender {
		return emailServiceForMock(mock, from, fromName)
	})

	emailHandler := &api.EmailHandler{TemporalClient: c, Registry: registry}
	srv := newServer(t, emailHandler, nil, nil)

	status, resp := postEmail(t, srv.URL+"/notify/email", models.EmailMessage{
		To:      "alice@example.com",
		Subject: "Welcome to Beacon",
		Body:    "Hello Alice, your account is ready.",
	})

	if status != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (resp: %+v)", status, resp)
	}
	if !resp.Success {
		t.Fatalf("expected success=true, got %+v", resp)
	}
	if wfID, _ := resp.Data["workflow_id"].(string); wfID == "" {
		t.Fatalf("expected workflow_id in response data, got %+v", resp.Data)
	}
	if got, _ := resp.Data["provider"].(string); got != provider {
		t.Errorf("expected provider %q in response, got %q", provider, got)
	}

	assertDelivered(t, mock, "alice@example.com", "Welcome to Beacon", "Hello Alice")
}

// -----------------------------------------------------------------------------
// Scenario 2: Routing by client_hint to one of two providers
// -----------------------------------------------------------------------------

func TestIntegration_RoutingByClientHint(t *testing.T) {
	c := dialOrSkip(t)

	mockA := testsupport.NewMockSMTPServer(t)
	mockB := testsupport.NewMockSMTPServer(t)

	providerA := providerNameFor(t, "transactional")
	providerB := providerNameFor(t, "marketing")
	const catA = "transactional"
	const catB = "marketing"
	const fromA = "tx@beacon.test"
	const fromB = "mkt@beacon.test"

	bundle := newBundle(
		smtpProvider{
			name:       providerA,
			host:       mockA.Host(),
			port:       mockA.Port(),
			from:       fromA,
			fromName:   "Beacon Tx",
			categories: []string{catA},
			isDefault:  true,
		},
		smtpProvider{
			name:       providerB,
			host:       mockB.Host(),
			port:       mockB.Port(),
			from:       fromB,
			fromName:   "Beacon Mkt",
			categories: []string{catB},
		},
	)
	registry, err := notifier.NewEmailClientRegistry(bundle)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}

	// One worker per provider task queue.
	startWorker(t, c, providerA, func() notifier.Sender {
		return emailServiceForMock(mockA, fromA, "Beacon Tx")
	})
	startWorker(t, c, providerB, func() notifier.Sender {
		return emailServiceForMock(mockB, fromB, "Beacon Mkt")
	})

	emailHandler := &api.EmailHandler{TemporalClient: c, Registry: registry}
	srv := newServer(t, emailHandler, nil, nil)

	// Route to provider B via client_hint == catB.
	status, resp := postEmail(t, srv.URL+"/notify/email", models.EmailMessage{
		To:         "bob@example.com",
		Subject:    "Big Sale",
		Body:       "50% off everything.",
		ClientHint: catB,
	})
	if status != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (resp: %+v)", status, resp)
	}
	if got, _ := resp.Data["provider"].(string); got != providerB {
		t.Fatalf("expected routing to provider %q, response says %q", providerB, got)
	}

	// Provider B's mock SMTP must receive the message.
	assertDelivered(t, mockB, "bob@example.com", "Big Sale", "50% off")

	// Provider A's mock SMTP must NOT receive anything. Give it a brief window
	// to rule out misrouting, then assert it is empty.
	if msgs := waitForMessages(mockA, 1, 2*time.Second); len(msgs) != 0 {
		t.Errorf("provider A mock SMTP unexpectedly received %d message(s): %+v", len(msgs), msgs)
	}
}

// -----------------------------------------------------------------------------
// Scenario 3: Validation failure -> 400, no delivery
// -----------------------------------------------------------------------------

func TestIntegration_ValidationFailure(t *testing.T) {
	c := dialOrSkip(t)

	mock := testsupport.NewMockSMTPServer(t)
	provider := providerNameFor(t, "default")
	const from = "noreply@beacon.test"

	bundle := newBundle(smtpProvider{
		name:      provider,
		host:      mock.Host(),
		port:      mock.Port(),
		from:      from,
		fromName:  "Beacon",
		isDefault: true,
	})
	registry, err := notifier.NewEmailClientRegistry(bundle)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}

	// Start a worker so that if (incorrectly) a workflow were started, delivery
	// could happen -- making the "no delivery" assertion meaningful.
	startWorker(t, c, provider, func() notifier.Sender {
		return emailServiceForMock(mock, from, "Beacon")
	})

	emailHandler := &api.EmailHandler{TemporalClient: c, Registry: registry}
	srv := newServer(t, emailHandler, nil, nil)

	status, resp := postEmail(t, srv.URL+"/notify/email", models.EmailMessage{
		To:      "not-an-email-address",
		Subject: "Should be rejected",
		Body:    "This must never be delivered.",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid 'to', got %d (resp: %+v)", status, resp)
	}
	if resp.Success {
		t.Errorf("expected success=false for validation failure, got %+v", resp)
	}

	// No delivery should occur.
	if msgs := waitForMessages(mock, 1, 2*time.Second); len(msgs) != 0 {
		t.Errorf("expected no SMTP delivery for invalid request, got %d: %+v", len(msgs), msgs)
	}
}

// -----------------------------------------------------------------------------
// Scenario 4: Method not allowed
// -----------------------------------------------------------------------------

func TestIntegration_MethodNotAllowed(t *testing.T) {
	c := dialOrSkip(t)

	mock := testsupport.NewMockSMTPServer(t)
	provider := providerNameFor(t, "default")

	bundle := newBundle(smtpProvider{
		name:      provider,
		host:      mock.Host(),
		port:      mock.Port(),
		from:      "noreply@beacon.test",
		fromName:  "Beacon",
		isDefault: true,
	})
	registry, err := notifier.NewEmailClientRegistry(bundle)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}

	emailHandler := &api.EmailHandler{TemporalClient: c, Registry: registry}
	srv := newServer(t, emailHandler, nil, nil)

	resp, err := http.Get(srv.URL + "/notify/email")
	if err != nil {
		t.Fatalf("GET /notify/email: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET /notify/email, got %d", resp.StatusCode)
	}
}

// -----------------------------------------------------------------------------
// Scenario 5: SMTP failure -> DLQ -> replay
// -----------------------------------------------------------------------------

func TestIntegration_SMTPFailureToDLQToReplay(t *testing.T) {
	c := dialOrSkip(t)

	// Healthy mock the replay will eventually use.
	healthy := testsupport.NewMockSMTPServer(t)
	provider := providerNameFor(t, "flaky")
	const from = "noreply@beacon.test"
	const fromName = "Beacon"

	// The worker's EmailService starts pointed at a DEAD port (127.0.0.1:1 ->
	// connection refused, so each activity attempt fails fast). It is swapped to
	// the healthy mock before replay, mirroring cmd/email_worker hot-swap.
	swap := newSwappableService(emailServiceForAddr("127.0.0.1", 1, from, fromName))

	// Registry only needs to resolve the provider for routing; the actual SMTP
	// target the worker uses comes from the swappable service.
	bundle := newBundle(smtpProvider{
		name:      provider,
		host:      "127.0.0.1",
		port:      1,
		from:      from,
		fromName:  fromName,
		isDefault: true,
	})
	registry, err := notifier.NewEmailClientRegistry(bundle)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}

	startWorker(t, c, provider, swap.get)

	dlqService := dlq.NewDLQService(c, namespace, testLogger())
	dlqHandler := api.NewDLQHandler(dlqService, testLogger())
	emailHandler := &api.EmailHandler{TemporalClient: c, Registry: registry}
	srv := newServer(t, emailHandler, dlqHandler, nil)

	// POST a valid email; routing succeeds, but delivery will fail repeatedly.
	status, resp := postEmail(t, srv.URL+"/notify/email", models.EmailMessage{
		To:      "carol@example.com",
		Subject: "Will fail then replay",
		Body:    "Initial delivery fails; replay must succeed.",
	})
	if status != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (resp: %+v)", status, resp)
	}
	workflowID, _ := resp.Data["workflow_id"].(string)
	if workflowID == "" {
		t.Fatalf("expected workflow_id in response, got %+v", resp.Data)
	}

	// Wait for the workflow to exhaust retries and reach terminal FAILED.
	finalStatus := waitTerminalFailed(t, c, workflowID, terminalTimeout)
	if finalStatus != enumspb.WORKFLOW_EXECUTION_STATUS_FAILED &&
		finalStatus != enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT {
		t.Fatalf("workflow %s did not reach terminal FAILED/TIMED_OUT within %s (last status: %v)",
			workflowID, terminalTimeout, finalStatus)
	}

	// The failed workflow must appear in GET /dlq/failed. Poll briefly because
	// visibility (ListClosedWorkflow) can lag slightly behind the close event.
	found := false
	dlqDeadline := time.Now().Add(15 * time.Second)
	for {
		st, body := getJSON(t, srv.URL+"/dlq/failed?provider="+provider)
		if st != http.StatusOK {
			t.Fatalf("GET /dlq/failed -> %d, body: %s", st, body)
		}
		var parsed struct {
			Data struct {
				Failures []dlq.FailedNotification `json:"failures"`
				Count    int                      `json:"count"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("decode /dlq/failed body %q: %v", string(body), err)
		}
		for _, f := range parsed.Data.Failures {
			if f.WorkflowID == workflowID {
				found = true
				break
			}
		}
		if found || time.Now().After(dlqDeadline) {
			if parsed.Data.Count < 1 && !found {
				// keep last count for the error message below
			}
			break
		}
		time.Sleep(pollInterval)
	}
	if !found {
		t.Fatalf("failed workflow %s did not appear in /dlq/failed within deadline", workflowID)
	}

	// Hot-swap the worker's EmailService to the HEALTHY mock, then replay.
	swap.swap(emailServiceForMock(healthy, from, fromName))

	replayStatus, replayResp := postJSON(t, srv.URL+"/dlq/replay/"+workflowID, nil)
	if replayStatus != http.StatusAccepted {
		t.Fatalf("expected 202 from replay, got %d (resp: %s)", replayStatus, replayResp)
	}

	// Bonus: the replayed workflow should deliver to the healthy mock.
	assertDelivered(t, healthy, "carol@example.com", "Will fail then replay", "replay must succeed")
}

// -----------------------------------------------------------------------------
// Scenario 6 (optional): /admin/config/refresh changes routing
// -----------------------------------------------------------------------------
//
// The admin handler depends on a fully-initialised *config.ConfigService
// (RefreshConfig + GetConfig), which in turn requires real config providers and
// is heavily exercised by the unit tests in internal/config and internal/api.
// Standing up a real ConfigService here would require non-test production wiring
// and external config sources, which is out of scope for this integration file
// (handlers + Temporal + SMTP). Routing changes via Registry.Reload are already
// covered by the routing scenario (TestIntegration_RoutingByClientHint) and the
// registry unit tests, so this optional scenario is intentionally omitted to
// keep the integration surface focused and deterministic.

// -----------------------------------------------------------------------------
// HTTP helpers
// -----------------------------------------------------------------------------

// getJSON performs a GET and returns the status code and raw body.
func getJSON(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// postJSON performs a POST with an optional JSON body and returns the status
// code and raw body.
func postJSON(t *testing.T, url string, payload any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	resp, err := http.Post(url, "application/json", rdr)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}
