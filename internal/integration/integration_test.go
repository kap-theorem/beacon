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
	"beacon/internal/auth"
	"beacon/internal/channel"
	"beacon/internal/config"
	"beacon/internal/dlq"
	"beacon/internal/notifier"
	"beacon/internal/policy"
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

	// dlqAdminToken authenticates the DLQ-replay scenario's requests against
	// the /v1/dlq/* routes, which Task 10 put behind auth.Middleware. Setting
	// ADMIN_TOKEN to this value lets the test act as an unscoped operator
	// without needing a full auth.Registry/service bundle.
	dlqAdminToken = "integration-test-admin-token"

	// testAPIKey authenticates the notify scenarios' requests against the
	// authenticated /v1/notify/{channel} route. Task 12 removed the
	// unauthenticated /notify/email route, so these tests now present a real
	// API key (see newBundle) exactly like production traffic.
	testAPIKey = "bk_it1_integrationtestsecret"
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
// Temporal task queue via channel.TaskQueue.
func providerNameFor(t *testing.T, label string) string {
	t.Helper()
	base := strings.ToLower(t.Name())
	base = strings.NewReplacer("/", "-", "_", "-", " ", "-").Replace(base)
	return fmt.Sprintf("%s-%s-%s", base, label, uniqueSuffix())
}

// smtpProvider describes one mock SMTP backend and its routing config.
type smtpProvider struct {
	name      string
	host      string
	port      int
	from      string
	fromName  string
	isDefault bool
}

// newBundle builds a *config.ConfigBundle from the given providers, plus a
// single test service ("integration-test", authenticated via testAPIKey)
// whose email-channel policy allows every listed provider. Task 12 removed
// the unauthenticated /notify/email route and notifier.EmailClientRegistry;
// these tests now authenticate and route exactly like real v2 traffic
// (auth.Registry + notifier.ProviderRegistry + policy.ResolveProvider).
func newBundle(providers ...smtpProvider) *config.ConfigBundle {
	smtp := make(map[string]*config.SMTPClientConfig, len(providers))
	names := make([]string, 0, len(providers))
	defaultProvider := ""
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
		}
		names = append(names, p.name)
		if p.isDefault || defaultProvider == "" {
			defaultProvider = p.name
		}
	}
	return &config.ConfigBundle{
		SMTP: smtp,
		Services: map[string]*config.ServiceConfig{
			"integration-test": {
				Service: "integration-test", Tenant: "it", Enabled: true,
				Keys: []config.KeyEntry{{ID: "it1", SHA256: auth.HashKey(testAPIKey), State: "active"}},
				Channels: map[string]*config.ChannelPolicy{
					"email": {
						Providers:       names,
						DefaultProvider: defaultProvider,
						Rate:            config.RateConfig{RPM: 1000, Daily: 100000},
					},
				},
			},
		},
		Revision:  1,
		Timestamp: time.Now(),
	}
}

// serviceSpec describes one authenticated calling service for
// newBundleWithServices: a name, tenant, API key, channel policy (provider
// allowlist/default), rate limit, and optional sender-identity lock. newBundle
// above covers the common single-service case with a fixed generous rate and
// no From-lock; scenarios that need custom rate limits, a policy-locked From
// identity, or multiple tenants (for DLQ scoping) use this instead.
type serviceSpec struct {
	service         string
	tenant          string
	apiKey          string
	providers       []string
	defaultProvider string
	rate            config.RateConfig
	from            *config.FromIdentity
}

// newBundleWithServices builds a *config.ConfigBundle from the given SMTP
// providers and one-or-more serviceSpec fixtures, generalizing newBundle to
// support more than one authenticated service/tenant per bundle.
func newBundleWithServices(providers []smtpProvider, services ...serviceSpec) *config.ConfigBundle {
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
		}
	}
	svcs := make(map[string]*config.ServiceConfig, len(services))
	for _, s := range services {
		svcs[s.service] = &config.ServiceConfig{
			Service: s.service, Tenant: s.tenant, Enabled: true,
			Keys: []config.KeyEntry{{ID: s.service, SHA256: auth.HashKey(s.apiKey), State: "active"}},
			Channels: map[string]*config.ChannelPolicy{
				"email": {
					Providers:       s.providers,
					DefaultProvider: s.defaultProvider,
					From:            s.from,
					Rate:            s.rate,
				},
			},
		}
	}
	return &config.ConfigBundle{
		SMTP:      smtp,
		Services:  svcs,
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
	tq := channel.TaskQueue("email", providerName)
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
// notifyHandler / dlqHandler / adminHandler may be nil to omit that route.
// authReg is required whenever notifyHandler is non-nil (it authenticates
// the /v1/notify/{channel} route, see auth.Middleware).
//
// The notify and DLQ routes are wired at their production v1 paths behind
// auth.Middleware (Task 10 made /v1/dlq/* authenticated and tenant-scoped;
// Task 12 removed the unauthenticated /notify/email route in favor of the
// authenticated /v1/notify/{channel}). Mirroring internal/app/server.go's
// BuildServerMux, the DLQ routes authenticate through the SAME authReg as
// notify -- not a separate empty registry -- so tenant-scoped service keys
// (not just the ADMIN_TOKEN override baked into auth.Middleware) can
// exercise /v1/dlq/* exactly like production. Notify callers normally
// present testAPIKey (see newBundle/postNotify); DLQ callers may present
// either dlqAdminToken via ADMIN_TOKEN (see postJSON/getJSON) or a real
// service key from authReg's bundle (see postJSONAs/getJSONAs).
func newServer(t *testing.T, notifyHandler *api.NotifyHandler, authReg *auth.Registry, dlqHandler *api.DLQHandler, adminHandler *api.AdminHandler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	if notifyHandler != nil {
		authMW := auth.Middleware(authReg)
		mux.Handle("POST /v1/notify/{channel}", authMW(http.HandlerFunc(notifyHandler.Handle)))
	}
	if dlqHandler != nil {
		dlqAuthReg := authReg
		if dlqAuthReg == nil {
			dlqAuthReg = auth.NewRegistry(nil)
		}
		authMW := auth.Middleware(dlqAuthReg)
		mux.Handle("GET /v1/dlq/failed", authMW(http.HandlerFunc(dlqHandler.HandleQueryFailures)))
		mux.Handle("POST /v1/dlq/replay/{workflowID}", authMW(http.HandlerFunc(dlqHandler.HandleReplay)))
	}
	if adminHandler != nil {
		mux.HandleFunc("/admin/config/refresh", adminHandler.HandleConfigRefresh)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// notifyRequest mirrors the authenticated /v1/notify/email request body (see
// internal/channel/email.go's emailRequest). Provider is optional: empty
// means "use the service's policy default" (policy.ResolveProvider).
type notifyRequest struct {
	To       string `json:"to"`
	Subject  string `json:"subject"`
	Body     string `json:"body"`
	Provider string `json:"provider,omitempty"`
}

// doNotify POSTs a notifyRequest JSON body, authenticated with apiKey (unless
// empty, to exercise the unauthenticated path) plus any extraHeaders (e.g.
// Idempotency-Key), to the given URL (normally .../v1/notify/email). It
// returns the status code, decoded API response, and response headers (for
// scenarios that assert on headers like Retry-After). postNotify wraps this
// for the common case where headers and extraHeaders are not needed.
func doNotify(t *testing.T, url, apiKey string, msg notifyRequest, extraHeaders map[string]string) (int, utils_APIResponse, http.Header) {
	t.Helper()
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal notify request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build POST %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
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
	return resp.StatusCode, out, resp.Header
}

// postNotify POSTs a notifyRequest JSON body, authenticated with apiKey, to
// the given URL (normally .../v1/notify/email) and returns the status code
// and decoded API response.
func postNotify(t *testing.T, url, apiKey string, msg notifyRequest) (int, utils_APIResponse) {
	t.Helper()
	status, resp, _ := doNotify(t, url, apiKey, msg, nil)
	return status, resp
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
	authReg := auth.NewRegistry(bundle)
	providers := notifier.NewProviderRegistry(bundle)

	startWorker(t, c, provider, func() notifier.Sender {
		return emailServiceForMock(mock, from, fromName)
	})

	notifyHandler := &api.NotifyHandler{
		TemporalClient: c, Channels: channel.NewRegistry(),
		Providers: providers, Limiter: policy.NewMemoryLimiter(nil), Logger: testLogger(),
	}
	srv := newServer(t, notifyHandler, authReg, nil, nil)

	status, resp := postNotify(t, srv.URL+"/v1/notify/email", testAPIKey, notifyRequest{
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
// Scenario 2: Routing to one of two allow-listed providers via the explicit
// "provider" field. Task 12 removed client_hint / category-based routing
// (notifier.EmailClientRegistry) along with the unauthenticated route; the
// v2 surface routes by an explicit, policy-checked provider binding instead
// (policy.ResolveProvider), which this test now exercises.
// -----------------------------------------------------------------------------

func TestIntegration_RoutingByProvider(t *testing.T) {
	c := dialOrSkip(t)

	mockA := testsupport.NewMockSMTPServer(t)
	mockB := testsupport.NewMockSMTPServer(t)

	providerA := providerNameFor(t, "transactional")
	providerB := providerNameFor(t, "marketing")
	const fromA = "tx@beacon.test"
	const fromB = "mkt@beacon.test"

	bundle := newBundle(
		smtpProvider{
			name:      providerA,
			host:      mockA.Host(),
			port:      mockA.Port(),
			from:      fromA,
			fromName:  "Beacon Tx",
			isDefault: true,
		},
		smtpProvider{
			name:     providerB,
			host:     mockB.Host(),
			port:     mockB.Port(),
			from:     fromB,
			fromName: "Beacon Mkt",
		},
	)
	authReg := auth.NewRegistry(bundle)
	providers := notifier.NewProviderRegistry(bundle)

	// One worker per provider task queue.
	startWorker(t, c, providerA, func() notifier.Sender {
		return emailServiceForMock(mockA, fromA, "Beacon Tx")
	})
	startWorker(t, c, providerB, func() notifier.Sender {
		return emailServiceForMock(mockB, fromB, "Beacon Mkt")
	})

	notifyHandler := &api.NotifyHandler{
		TemporalClient: c, Channels: channel.NewRegistry(),
		Providers: providers, Limiter: policy.NewMemoryLimiter(nil), Logger: testLogger(),
	}
	srv := newServer(t, notifyHandler, authReg, nil, nil)

	// Route to provider B via the explicit "provider" field.
	status, resp := postNotify(t, srv.URL+"/v1/notify/email", testAPIKey, notifyRequest{
		To:       "bob@example.com",
		Subject:  "Big Sale",
		Body:     "50% off everything.",
		Provider: providerB,
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
	authReg := auth.NewRegistry(bundle)
	providers := notifier.NewProviderRegistry(bundle)

	// Start a worker so that if (incorrectly) a workflow were started, delivery
	// could happen -- making the "no delivery" assertion meaningful.
	startWorker(t, c, provider, func() notifier.Sender {
		return emailServiceForMock(mock, from, "Beacon")
	})

	notifyHandler := &api.NotifyHandler{
		TemporalClient: c, Channels: channel.NewRegistry(),
		Providers: providers, Limiter: policy.NewMemoryLimiter(nil), Logger: testLogger(),
	}
	srv := newServer(t, notifyHandler, authReg, nil, nil)

	status, resp := postNotify(t, srv.URL+"/v1/notify/email", testAPIKey, notifyRequest{
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
	authReg := auth.NewRegistry(bundle)
	providers := notifier.NewProviderRegistry(bundle)

	notifyHandler := &api.NotifyHandler{
		TemporalClient: c, Channels: channel.NewRegistry(),
		Providers: providers, Limiter: policy.NewMemoryLimiter(nil), Logger: testLogger(),
	}
	srv := newServer(t, notifyHandler, authReg, nil, nil)

	// The route is registered as "POST /v1/notify/{channel}"; Go's ServeMux
	// rejects a method mismatch with 405 at the mux level, before auth.Middleware
	// even runs -- so this requires no Authorization header.
	resp, err := http.Get(srv.URL + "/v1/notify/email")
	if err != nil {
		t.Fatalf("GET /v1/notify/email: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET /v1/notify/email, got %d", resp.StatusCode)
	}
}

// -----------------------------------------------------------------------------
// Scenario 5: SMTP failure -> DLQ -> replay
// -----------------------------------------------------------------------------

func TestIntegration_SMTPFailureToDLQToReplay(t *testing.T) {
	c := dialOrSkip(t)
	t.Setenv("ADMIN_TOKEN", dlqAdminToken)

	// Healthy mock the replay will eventually use.
	healthy := testsupport.NewMockSMTPServer(t)
	provider := providerNameFor(t, "flaky")
	const from = "noreply@beacon.test"
	const fromName = "Beacon"

	// The worker's EmailService starts pointed at a DEAD port (127.0.0.1:1 ->
	// connection refused, so each activity attempt fails fast). It is swapped to
	// the healthy mock before replay, mirroring cmd/email_worker hot-swap.
	swap := newSwappableService(emailServiceForAddr("127.0.0.1", 1, from, fromName))

	// The provider registry only needs to resolve the provider for routing;
	// the actual SMTP target the worker uses comes from the swappable service.
	bundle := newBundle(smtpProvider{
		name:      provider,
		host:      "127.0.0.1",
		port:      1,
		from:      from,
		fromName:  fromName,
		isDefault: true,
	})
	authReg := auth.NewRegistry(bundle)
	providers := notifier.NewProviderRegistry(bundle)

	startWorker(t, c, provider, swap.get)

	dlqService := dlq.NewDLQService(c, namespace, testLogger())
	dlqHandler := api.NewDLQHandler(dlqService, testLogger())
	notifyHandler := &api.NotifyHandler{
		TemporalClient: c, Channels: channel.NewRegistry(),
		Providers: providers, Limiter: policy.NewMemoryLimiter(nil), Logger: testLogger(),
	}
	srv := newServer(t, notifyHandler, authReg, dlqHandler, nil)

	// POST a valid email; routing succeeds, but delivery will fail repeatedly.
	status, resp := postNotify(t, srv.URL+"/v1/notify/email", testAPIKey, notifyRequest{
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
		st, body := getJSON(t, srv.URL+"/v1/dlq/failed?provider="+provider)
		if st != http.StatusOK {
			t.Fatalf("GET /v1/dlq/failed -> %d, body: %s", st, body)
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
		t.Fatalf("failed workflow %s did not appear in /v1/dlq/failed within deadline", workflowID)
	}

	// Hot-swap the worker's EmailService to the HEALTHY mock, then replay.
	swap.swap(emailServiceForMock(healthy, from, fromName))

	replayStatus, replayResp := postJSON(t, srv.URL+"/v1/dlq/replay/"+workflowID, nil)
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
// (handlers + Temporal + SMTP). Routing changes via ProviderRegistry.Reload are
// already covered by the routing scenario (TestIntegration_RoutingByProvider)
// and the registry unit tests, so this optional scenario is intentionally
// omitted to keep the integration surface focused and deterministic.

// -----------------------------------------------------------------------------
// Scenario 7: Unauthenticated request -> 401, no workflow ever started
// -----------------------------------------------------------------------------

func TestIntegration_Unauthenticated401(t *testing.T) {
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
	authReg := auth.NewRegistry(bundle)
	providers := notifier.NewProviderRegistry(bundle)

	// Start a worker so that if (incorrectly) a workflow were started, delivery
	// could happen -- making the "no delivery" assertion meaningful.
	startWorker(t, c, provider, func() notifier.Sender {
		return emailServiceForMock(mock, from, "Beacon")
	})

	notifyHandler := &api.NotifyHandler{
		TemporalClient: c, Channels: channel.NewRegistry(),
		Providers: providers, Limiter: policy.NewMemoryLimiter(nil), Logger: testLogger(),
	}
	srv := newServer(t, notifyHandler, authReg, nil, nil)

	// No Authorization header at all (apiKey "" tells postNotify to omit it).
	status, resp := postNotify(t, srv.URL+"/v1/notify/email", "", notifyRequest{
		To:      "dave@example.com",
		Subject: "Should be rejected",
		Body:    "No API key presented; must never be delivered.",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no Authorization header, got %d (resp: %+v)", status, resp)
	}
	if resp.Success {
		t.Errorf("expected success=false, got %+v", resp)
	}
	if !strings.Contains(resp.Error, "missing API key") {
		t.Errorf(`expected error to contain "missing API key", got %q`, resp.Error)
	}

	// No workflow should ever have started, so no delivery -- poll briefly.
	if msgs := waitForMessages(mock, 1, 2*time.Second); len(msgs) != 0 {
		t.Errorf("expected no SMTP delivery for unauthenticated request, got %d: %+v", len(msgs), msgs)
	}
}

// -----------------------------------------------------------------------------
// Scenario 8: Provider outside the service's allowlist -> 403
// -----------------------------------------------------------------------------

func TestIntegration_ForeignProvider403(t *testing.T) {
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
	authReg := auth.NewRegistry(bundle)
	providers := notifier.NewProviderRegistry(bundle)

	startWorker(t, c, provider, func() notifier.Sender {
		return emailServiceForMock(mock, from, "Beacon")
	})

	notifyHandler := &api.NotifyHandler{
		TemporalClient: c, Channels: channel.NewRegistry(),
		Providers: providers, Limiter: policy.NewMemoryLimiter(nil), Logger: testLogger(),
	}
	srv := newServer(t, notifyHandler, authReg, nil, nil)

	status, resp := postNotify(t, srv.URL+"/v1/notify/email", testAPIKey, notifyRequest{
		To:       "erin@example.com",
		Subject:  "Should be rejected",
		Body:     "Provider not in this service's allowlist.",
		Provider: "not-in-allowlist",
	})
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 for a provider outside the allowlist, got %d (resp: %+v)", status, resp)
	}
	if !strings.Contains(resp.Error, "not allowed") {
		t.Errorf(`expected error to contain "not allowed", got %q`, resp.Error)
	}

	if msgs := waitForMessages(mock, 1, 2*time.Second); len(msgs) != 0 {
		t.Errorf("expected no SMTP delivery for foreign-provider request, got %d: %+v", len(msgs), msgs)
	}
}

// -----------------------------------------------------------------------------
// Scenario 9: Rate limit burst exhausted -> 429 with Retry-After
// -----------------------------------------------------------------------------

func TestIntegration_RateLimit429(t *testing.T) {
	c := dialOrSkip(t)

	mock := testsupport.NewMockSMTPServer(t)
	provider := providerNameFor(t, "default")
	const from = "noreply@beacon.test"

	// rpm=2 means the token bucket starts with exactly 2 tokens (see
	// policy.MemoryLimiter.Allow), so 2 immediate requests are allowed and a
	// third, sent back-to-back, is not.
	bundle := newBundleWithServices(
		[]smtpProvider{{
			name: provider, host: mock.Host(), port: mock.Port(),
			from: from, fromName: "Beacon", isDefault: true,
		}},
		serviceSpec{
			service: "integration-test", tenant: "it", apiKey: testAPIKey,
			providers: []string{provider}, defaultProvider: provider,
			rate: config.RateConfig{RPM: 2, Daily: 1000},
		},
	)
	authReg := auth.NewRegistry(bundle)
	providers := notifier.NewProviderRegistry(bundle)

	startWorker(t, c, provider, func() notifier.Sender {
		return emailServiceForMock(mock, from, "Beacon")
	})

	// A fresh *policy.MemoryLimiter per test, matching the harness's
	// per-server-instance isolation (the in-memory limiter is scoped to the
	// process running it, exactly like a single production server instance).
	notifyHandler := &api.NotifyHandler{
		TemporalClient: c, Channels: channel.NewRegistry(),
		Providers: providers, Limiter: policy.NewMemoryLimiter(nil), Logger: testLogger(),
	}
	srv := newServer(t, notifyHandler, authReg, nil, nil)

	msg := notifyRequest{To: "frank@example.com", Subject: "Rate me", Body: "hello"}

	status1, resp1 := postNotify(t, srv.URL+"/v1/notify/email", testAPIKey, msg)
	if status1 != http.StatusAccepted {
		t.Fatalf("expected 202 for request 1/2 within the rpm=2 burst, got %d (resp: %+v)", status1, resp1)
	}
	status2, resp2 := postNotify(t, srv.URL+"/v1/notify/email", testAPIKey, msg)
	if status2 != http.StatusAccepted {
		t.Fatalf("expected 202 for request 2/2 within the rpm=2 burst, got %d (resp: %+v)", status2, resp2)
	}

	status3, resp3, hdr3 := doNotify(t, srv.URL+"/v1/notify/email", testAPIKey, msg, nil)
	if status3 != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for request 3 (rpm=2 burst exhausted), got %d (resp: %+v)", status3, resp3)
	}
	if hdr3.Get("Retry-After") == "" {
		t.Errorf("expected a Retry-After header on the 429 response, got none (headers: %+v)", hdr3)
	}
}

// -----------------------------------------------------------------------------
// Scenario 10: Idempotency-Key dedupe -> exactly one delivery
// -----------------------------------------------------------------------------

func TestIntegration_IdempotentDuplicate(t *testing.T) {
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
	authReg := auth.NewRegistry(bundle)
	providers := notifier.NewProviderRegistry(bundle)

	startWorker(t, c, provider, func() notifier.Sender {
		return emailServiceForMock(mock, from, "Beacon")
	})

	notifyHandler := &api.NotifyHandler{
		TemporalClient: c, Channels: channel.NewRegistry(),
		Providers: providers, Limiter: policy.NewMemoryLimiter(nil), Logger: testLogger(),
	}
	srv := newServer(t, notifyHandler, authReg, nil, nil)

	idemKey := "idem-" + uniqueSuffix()
	msg := notifyRequest{To: "grace@example.com", Subject: "Only once", Body: "Idempotent send."}
	headers := map[string]string{"Idempotency-Key": idemKey}

	status1, resp1, _ := doNotify(t, srv.URL+"/v1/notify/email", testAPIKey, msg, headers)
	if status1 != http.StatusAccepted {
		t.Fatalf("expected 202 for the first request, got %d (resp: %+v)", status1, resp1)
	}
	if dup, _ := resp1.Data["duplicate"].(bool); dup {
		t.Errorf("expected duplicate=false on the first request, got %+v", resp1.Data)
	}

	status2, resp2, _ := doNotify(t, srv.URL+"/v1/notify/email", testAPIKey, msg, headers)
	if status2 != http.StatusAccepted {
		t.Fatalf("expected 202 for the second (duplicate) request, got %d (resp: %+v)", status2, resp2)
	}
	if dup, _ := resp2.Data["duplicate"].(bool); !dup {
		t.Errorf("expected duplicate=true on the second request, got %+v", resp2.Data)
	}

	// Exactly one message must be delivered: wait for the first delivery, then
	// a short negative window rules out a second.
	if msgs := waitForMessages(mock, 1, deliverTimeout); len(msgs) == 0 {
		t.Fatalf("expected at least one delivered message, got none within %s", deliverTimeout)
	}
	if msgs := waitForMessages(mock, 2, 2*time.Second); len(msgs) != 1 {
		t.Errorf("expected exactly one delivered message, got %d: %+v", len(msgs), msgs)
	}
}

// -----------------------------------------------------------------------------
// Scenario 11: DLQ is tenant-scoped -- a tenant cannot see or replay another
// tenant's failed workflow.
// -----------------------------------------------------------------------------

func TestIntegration_DLQTenantScoping(t *testing.T) {
	c := dialOrSkip(t)

	provider := providerNameFor(t, "flaky")
	const from = "noreply@beacon.test"
	const fromName = "Beacon"
	const tenantAKey = "bk_dlqa1_tenantAsecret"
	const tenantBKey = "bk_dlqb1_tenantBsecret"

	// Tenant A's provider points at a dead port (127.0.0.1:1 -> connection
	// refused, so each activity attempt fails fast), mirroring
	// TestIntegration_SMTPFailureToDLQToReplay's mechanics; tenant B has no
	// provider access at all -- it only ever queries/replays DLQ endpoints.
	bundle := newBundleWithServices(
		[]smtpProvider{{
			name: provider, host: "127.0.0.1", port: 1,
			from: from, fromName: fromName, isDefault: true,
		}},
		serviceSpec{
			service: "tenant-a-service", tenant: "tenant-a", apiKey: tenantAKey,
			providers: []string{provider}, defaultProvider: provider,
			rate: config.RateConfig{RPM: 1000, Daily: 100000},
		},
		serviceSpec{
			service: "tenant-b-service", tenant: "tenant-b", apiKey: tenantBKey,
			rate: config.RateConfig{RPM: 1000, Daily: 100000},
		},
	)
	authReg := auth.NewRegistry(bundle)
	providers := notifier.NewProviderRegistry(bundle)

	startWorker(t, c, provider, func() notifier.Sender {
		return emailServiceForAddr("127.0.0.1", 1, from, fromName)
	})

	dlqService := dlq.NewDLQService(c, namespace, testLogger())
	dlqHandler := api.NewDLQHandler(dlqService, testLogger())
	notifyHandler := &api.NotifyHandler{
		TemporalClient: c, Channels: channel.NewRegistry(),
		Providers: providers, Limiter: policy.NewMemoryLimiter(nil), Logger: testLogger(),
	}
	srv := newServer(t, notifyHandler, authReg, dlqHandler, nil)

	// Tenant A sends a notification whose delivery will fail repeatedly.
	status, resp := postNotify(t, srv.URL+"/v1/notify/email", tenantAKey, notifyRequest{
		To:      "henry@example.com",
		Subject: "Tenant A failing send",
		Body:    "Will land in the DLQ.",
	})
	if status != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (resp: %+v)", status, resp)
	}
	workflowID, _ := resp.Data["workflow_id"].(string)
	if workflowID == "" {
		t.Fatalf("expected workflow_id in response, got %+v", resp.Data)
	}

	finalStatus := waitTerminalFailed(t, c, workflowID, terminalTimeout)
	if finalStatus != enumspb.WORKFLOW_EXECUTION_STATUS_FAILED &&
		finalStatus != enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT {
		t.Fatalf("workflow %s did not reach terminal FAILED/TIMED_OUT within %s (last status: %v)",
			workflowID, terminalTimeout, finalStatus)
	}

	// Poll (as tenant A) until the failure is visible at all, so a slow
	// ListClosedWorkflow index can't produce a false-negative "B can't see it"
	// result below.
	type failuresResp struct {
		Data struct {
			Failures []dlq.FailedNotification `json:"failures"`
		} `json:"data"`
	}
	foundForA := false
	dlqDeadline := time.Now().Add(15 * time.Second)
	for {
		st, body := getJSONAs(t, srv.URL+"/v1/dlq/failed?provider="+provider, tenantAKey)
		if st != http.StatusOK {
			t.Fatalf("GET /v1/dlq/failed (tenant A) -> %d, body: %s", st, body)
		}
		var parsed failuresResp
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("decode /dlq/failed body %q: %v", string(body), err)
		}
		for _, f := range parsed.Data.Failures {
			if f.WorkflowID == workflowID {
				foundForA = true
			}
		}
		if foundForA || time.Now().After(dlqDeadline) {
			break
		}
		time.Sleep(pollInterval)
	}
	if !foundForA {
		t.Fatalf("failed workflow %s did not appear in tenant A's /v1/dlq/failed within deadline", workflowID)
	}

	// Tenant B must not see tenant A's failure: the handler hard-scopes
	// filter.Tenant to the caller's own tenant for non-admin identities.
	stB, bodyB := getJSONAs(t, srv.URL+"/v1/dlq/failed?provider="+provider, tenantBKey)
	if stB != http.StatusOK {
		t.Fatalf("GET /v1/dlq/failed (tenant B) -> %d, body: %s", stB, bodyB)
	}
	var parsedB failuresResp
	if err := json.Unmarshal(bodyB, &parsedB); err != nil {
		t.Fatalf("decode /dlq/failed body %q: %v", string(bodyB), err)
	}
	for _, f := range parsedB.Data.Failures {
		if f.WorkflowID == workflowID {
			t.Fatalf("tenant B unexpectedly saw tenant A's failed workflow %s in DLQ results", workflowID)
		}
	}

	// Tenant B's replay attempt must not disclose the workflow's existence: 404.
	replayStatusB, replayBodyB := postJSONAs(t, srv.URL+"/v1/dlq/replay/"+workflowID, tenantBKey, nil)
	if replayStatusB != http.StatusNotFound {
		t.Fatalf("expected 404 replaying tenant A's workflow as tenant B, got %d (body: %s)", replayStatusB, replayBodyB)
	}

	// Tenant A's replay attempt must succeed (202) -- even though the
	// replayed send may fail again against the same dead port, only the
	// dispatch is asserted here.
	replayStatusA, replayBodyA := postJSONAs(t, srv.URL+"/v1/dlq/replay/"+workflowID, tenantAKey, nil)
	if replayStatusA != http.StatusAccepted {
		t.Fatalf("expected 202 replaying tenant A's own workflow, got %d (body: %s)", replayStatusA, replayBodyA)
	}
	var replayParsed struct {
		Success bool             `json:"success"`
		Data    dlq.ReplayResult `json:"data"`
	}
	if err := json.Unmarshal(replayBodyA, &replayParsed); err != nil {
		t.Fatalf("decode replay body %q: %v", string(replayBodyA), err)
	}
	if !replayParsed.Success {
		t.Fatalf("expected success=true for tenant A's replay, got %+v", replayParsed)
	}
	if replayParsed.Data.OriginalWorkflowID != workflowID {
		t.Errorf("expected original_workflow_id %q, got %q", workflowID, replayParsed.Data.OriginalWorkflowID)
	}
	if replayParsed.Data.NewWorkflowID == "" {
		t.Errorf("expected a non-empty new_workflow_id, got %+v", replayParsed.Data)
	}
	if replayParsed.Data.Provider != provider {
		t.Errorf("expected replay provider %q, got %q", provider, replayParsed.Data.Provider)
	}
}

// -----------------------------------------------------------------------------
// Scenario 12: Service policy's From lock overrides the provider's own
// configured sender identity on the delivered envelope.
// -----------------------------------------------------------------------------

func TestIntegration_PolicyFromLock(t *testing.T) {
	c := dialOrSkip(t)

	mock := testsupport.NewMockSMTPServer(t)
	provider := providerNameFor(t, "default")
	// Deliberately different from the policy lock below, so seeing
	// "locked@corp.com" on the wire proves the policy override won -- not
	// just the provider's own default passing through unchanged.
	const providerFrom = "provider-default@beacon.test"
	const lockedFrom = "locked@corp.com"

	bundle := newBundleWithServices(
		[]smtpProvider{{
			name: provider, host: mock.Host(), port: mock.Port(),
			from: providerFrom, fromName: "Provider Default", isDefault: true,
		}},
		serviceSpec{
			service: "integration-test", tenant: "it", apiKey: testAPIKey,
			providers: []string{provider}, defaultProvider: provider,
			rate: config.RateConfig{RPM: 1000, Daily: 100000},
			from: &config.FromIdentity{Address: lockedFrom, Name: "Locked"},
		},
	)
	authReg := auth.NewRegistry(bundle)
	providers := notifier.NewProviderRegistry(bundle)

	startWorker(t, c, provider, func() notifier.Sender {
		return emailServiceForMock(mock, providerFrom, "Provider Default")
	})

	notifyHandler := &api.NotifyHandler{
		TemporalClient: c, Channels: channel.NewRegistry(),
		Providers: providers, Limiter: policy.NewMemoryLimiter(nil), Logger: testLogger(),
	}
	srv := newServer(t, notifyHandler, authReg, nil, nil)

	status, resp := postNotify(t, srv.URL+"/v1/notify/email", testAPIKey, notifyRequest{
		To:      "ivy@example.com",
		Subject: "Locked sender identity",
		Body:    "From must be policy-locked regardless of provider defaults.",
	})
	if status != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (resp: %+v)", status, resp)
	}

	msgs := waitForMessages(mock, 1, deliverTimeout)
	if len(msgs) == 0 {
		t.Fatalf("expected at least one delivered message, got none within %s", deliverTimeout)
	}
	if got := msgs[0].From; got != lockedFrom {
		t.Errorf("expected envelope From %q (policy lock), got %q", lockedFrom, got)
	}
}

// -----------------------------------------------------------------------------
// HTTP helpers
// -----------------------------------------------------------------------------

// getJSON performs an authenticated GET as the admin token (see
// dlqAdminToken) and returns the status code and raw body. Currently only
// exercised against /v1/dlq/* routes.
func getJSON(t *testing.T, url string) (int, []byte) {
	t.Helper()
	return getJSONAs(t, url, dlqAdminToken)
}

// getJSONAs performs an authenticated GET with an arbitrary caller apiKey
// (a real service key, not necessarily the admin token) and returns the
// status code and raw body. Used by the DLQ tenant-scoping scenario to query
// /v1/dlq/failed as different tenants' service identities.
func getJSONAs(t *testing.T, url, apiKey string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// postJSON performs an authenticated POST as the admin token (see
// dlqAdminToken) with an optional JSON body and returns the status code and
// raw body. Currently only exercised against /v1/dlq/* routes.
func postJSON(t *testing.T, url string, payload any) (int, []byte) {
	t.Helper()
	return postJSONAs(t, url, dlqAdminToken, payload)
}

// postJSONAs performs an authenticated POST with an arbitrary caller apiKey
// (a real service key, not necessarily the admin token) and an optional JSON
// body, returning the status code and raw body. Used by the DLQ
// tenant-scoping scenario to attempt /v1/dlq/replay/* as different tenants'
// service identities.
func postJSONAs(t *testing.T, url, apiKey string, payload any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, url, rdr)
	if err != nil {
		t.Fatalf("build POST %s: %v", url, err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}
