# Beacon Deployment-Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Beacon production-deployable on a home server (self-hosted Temporal cluster + per-provider workers + Cloudflare Tunnel) and provably feature-complete (≥90% test coverage, simplified code, integration tests with mocked downstream services), plus author the deployment runbook, downstream integration guide, and future-scope auth design.

**Architecture:** Behavior-preserving testability refactors first (small interfaces for mocking the Temporal client and DLQ service; extract `cmd/*` logic into a testable `internal/app` package). Then a `/simplify` pass, then per-package TDD coverage to a hard 90% gate, then integration tests through the real HTTP handlers against an in-process mock SMTP server and Temporal's test environment. Finally, generate and locally validate deployment artifacts and write the docs.

**Tech Stack:** Go 1.24.4, Temporal Go SDK (`go.temporal.io/sdk` incl. `testsuite` + `mocks`), `gopkg.in/mail.v2` (SMTP), `stretchr/testify` (already in module graph), `net/http/httptest`, Docker Compose, `cloudflared`, Infisical.

---

## How to use this plan

The plan is organized into **phases**. Each phase ends at a green, committable state. Phases 0–1 are sequential (foundation + refactors). Phase 3 (coverage) fans out across packages and is parallelizable via agent teams (see the Orchestration Appendix). Phases 4–7 follow.

**Conventions for every coding task:** write the failing test first, run it to see it fail, implement, run it to see it pass, then commit. Run `gofmt`/`go vet ./...` before each commit. Never commit with failing tests.

**Coverage gate definition (referenced throughout):** every package under `./internal/...`, plus `./utils`, must reach **≥90.0% statement coverage**. The two `cmd/*` main packages are reduced to thin shells (Phase 1) and are exercised by the instrumented integration run in Phase 5 rather than by unit tests; they are excluded from the unit gate by the script in Task 0.2.

---

## Phase 0 — Test foundation & tooling

### Task 0.1: Promote test dependencies and add coverage tooling

**Files:**
- Modify: `go.mod`, `go.sum` (via `go get`)
- Create: `scripts/check-coverage.sh`
- Modify: `Makefile`
- Modify: `.gitignore`

- [ ] **Step 1: Promote testify to a direct dependency**

Run:
```bash
cd /Users/kushalkrishnappa/Playground/beacon
go get github.com/stretchr/testify@v1.10.0
```
Expected: `go.mod` moves `github.com/stretchr/testify` out of the indirect block (or adds it to the direct `require`).

- [ ] **Step 2: Create the coverage gate script**

Create `scripts/check-coverage.sh`:
```bash
#!/usr/bin/env bash
# Fails if any gated package is below the threshold.
# Gated = all ./internal/... and ./utils packages. cmd/* main shells are
# excluded here (covered by the instrumented integration run in Phase 5).
set -euo pipefail

THRESHOLD="${COVERAGE_THRESHOLD:-90.0}"
PROFILE="${COVERAGE_PROFILE:-coverage.out}"

# Packages to gate.
PKGS=$(go list ./internal/... ./utils 2>/dev/null)

go test -covermode=set -coverprofile="$PROFILE" $PKGS >/dev/null

echo "Per-package coverage (threshold ${THRESHOLD}%):"
fail=0
# func-level report aggregated to package totals.
while read -r pkg; do
  pct=$(go test -covermode=set -coverprofile=/tmp/p.out "$pkg" 2>/dev/null \
        | awk '/coverage:/ {gsub("%","",$0); for(i=1;i<=NF;i++) if($i=="coverage:"){print $(i+1)}}')
  if [ -z "$pct" ]; then pct="0.0"; fi
  awk -v p="$pct" -v t="$THRESHOLD" -v k="$pkg" \
    'BEGIN{ if (p+0 < t+0) { printf "  FAIL %6.1f%%  %s\n", p, k; exit 3 } else { printf "  ok   %6.1f%%  %s\n", p, k } }' \
    || fail=1
done <<< "$PKGS"

if [ "$fail" -ne 0 ]; then
  echo "Coverage gate FAILED: one or more packages below ${THRESHOLD}%." >&2
  exit 1
fi
echo "Coverage gate PASSED."
```
Then: `chmod +x scripts/check-coverage.sh`.

- [ ] **Step 3: Add Makefile targets**

Add to `Makefile` (append; keep existing `.PHONY` line consistent by extending it):
```makefile
.PHONY: test cover cover-html

test:
	go test ./...

cover:
	./scripts/check-coverage.sh

cover-html:
	go test -covermode=set -coverprofile=coverage.out ./internal/... ./utils
	go tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"
```

- [ ] **Step 4: Ignore coverage artifacts**

Add to `.gitignore`:
```
coverage.out
coverage.html
covdata/
```

- [ ] **Step 5: Verify tooling runs (expected to FAIL the gate now)**

Run: `make cover`
Expected: it executes and reports per-package percentages; the gate FAILS (most packages near 0%). This confirms the script works. That failure is expected at this stage.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum scripts/check-coverage.sh Makefile .gitignore
git commit -m "chore: add coverage gate tooling and promote testify"
```

### Task 0.2: Shared in-process mock SMTP server (test support)

This dependency-free SMTP sink is used by both `internal/notifier` unit tests and the Phase 4 integration tests. It speaks the minimal subset gomail uses (EHLO without STARTTLS/AUTH, MAIL/RCPT/DATA/QUIT) and captures delivered messages.

**Files:**
- Create: `internal/testsupport/smtpserver.go`
- Create: `internal/testsupport/smtpserver_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/testsupport/smtpserver_test.go`:
```go
package testsupport

import (
	"testing"

	gomail "gopkg.in/mail.v2"
)

func TestMockSMTPServer_CapturesMessage(t *testing.T) {
	srv := NewMockSMTPServer(t) // starts on a random localhost port, registers cleanup

	m := gomail.NewMessage()
	m.SetAddressHeader("From", "beacon@local", "Beacon")
	m.SetHeader("To", "alice@example.com")
	m.SetHeader("Subject", "hello")
	m.SetBody("text/plain", "world")

	d := gomail.NewDialer(srv.Host(), srv.Port(), "", "") // empty creds => no AUTH
	if err := d.DialAndSend(m); err != nil {
		t.Fatalf("DialAndSend: %v", err)
	}

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("want 1 captured message, got %d", len(msgs))
	}
	if msgs[0].To[0] != "alice@example.com" {
		t.Errorf("recipient: got %q", msgs[0].To[0])
	}
	if !contains(msgs[0].Data, "Subject: hello") {
		t.Errorf("subject not found in data: %q", msgs[0].Data)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/testsupport/ -run TestMockSMTPServer_CapturesMessage -v`
Expected: FAIL — `NewMockSMTPServer` undefined.

- [ ] **Step 3: Implement the mock SMTP server**

Create `internal/testsupport/smtpserver.go`:
```go
// Package testsupport provides shared test helpers (in-process mock SMTP server).
package testsupport

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
)

// CapturedMessage is a single message accepted by the mock server.
type CapturedMessage struct {
	From string
	To   []string
	Data string
}

// MockSMTPServer is a minimal in-process SMTP server for tests. It accepts the
// subset of SMTP that gopkg.in/mail.v2 uses with no auth and no STARTTLS.
type MockSMTPServer struct {
	ln       net.Listener
	mu       sync.Mutex
	messages []CapturedMessage
}

// NewMockSMTPServer starts the server on a random localhost port and registers
// cleanup with t.
func NewMockSMTPServer(t *testing.T) *MockSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &MockSMTPServer{ln: ln}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *MockSMTPServer) Host() string { return "127.0.0.1" }

func (s *MockSMTPServer) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Messages returns a copy of all captured messages.
func (s *MockSMTPServer) Messages() []CapturedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CapturedMessage, len(s.messages))
	copy(out, s.messages)
	return out
}

func (s *MockSMTPServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handle(conn)
	}
}

func (s *MockSMTPServer) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	write := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}

	write("220 mock.local ESMTP")
	var msg CapturedMessage
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			// Advertise nothing extra: no STARTTLS, no AUTH.
			write("250-mock.local")
			write("250 OK")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			msg.From = extractAddr(line)
			write("250 OK")
		case strings.HasPrefix(cmd, "RCPT TO"):
			msg.To = append(msg.To, extractAddr(line))
			write("250 OK")
		case cmd == "DATA":
			write("354 End data with <CR><LF>.<CR><LF>")
			var sb strings.Builder
			for {
				dl, derr := r.ReadString('\n')
				if derr != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				sb.WriteString(dl)
			}
			msg.Data = sb.String()
			s.mu.Lock()
			s.messages = append(s.messages, msg)
			s.mu.Unlock()
			msg = CapturedMessage{}
			write("250 OK: queued")
		case cmd == "RSET":
			msg = CapturedMessage{}
			write("250 OK")
		case cmd == "NOOP":
			write("250 OK")
		case cmd == "QUIT":
			write("221 Bye")
			return
		default:
			write("250 OK")
		}
	}
}

// extractAddr pulls the address out of "MAIL FROM:<addr>" / "RCPT TO:<addr>".
func extractAddr(line string) string {
	start := strings.Index(line, "<")
	end := strings.Index(line, ">")
	if start >= 0 && end > start {
		return line[start+1 : end]
	}
	return strings.TrimSpace(line)
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/testsupport/ -run TestMockSMTPServer_CapturesMessage -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/testsupport/
git commit -m "test: add in-process mock SMTP server test support"
```

---

## Phase 1 — Testability refactors (behavior-preserving) + drift fixes

> All changes here preserve runtime behavior. After each task, run `go build ./...` and `go test ./...` (existing tests must stay green).

### Task 1.1: Decouple the email handler from the concrete Temporal client

**Files:**
- Modify: `internal/api/email.go`
- Modify: `cmd/server/server.go:86-89` (field assignment unchanged in spirit)

- [ ] **Step 1: Introduce a minimal interface**

In `internal/api/email.go`, replace the `TemporalClient client.Client` field with a narrow interface the real client already satisfies:
```go
// WorkflowStarter is the slice of the Temporal client the email handler needs.
// *client.Client (the real Temporal client) satisfies this automatically.
type WorkflowStarter interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (client.WorkflowRun, error)
}

// EmailHandler handles email notification requests.
type EmailHandler struct {
	TemporalClient WorkflowStarter
	Registry       *notifier.EmailClientRegistry
}
```
Add `"context"` to imports. The `h.TemporalClient == nil` check still works (interface nil).

- [ ] **Step 2: Verify build and existing behavior**

Run: `go build ./...`
Expected: success. `cmd/server/server.go` assigns `temporalClient` (a `client.Client`) into the field — still compiles because `client.Client` satisfies `WorkflowStarter`.

- [ ] **Step 3: Commit**

```bash
git add internal/api/email.go
git commit -m "refactor: narrow email handler Temporal dependency to an interface"
```

### Task 1.2: Decouple the DLQ handler from the concrete service

**Files:**
- Modify: `internal/api/dlq.go`

- [ ] **Step 1: Introduce a service interface**

In `internal/api/dlq.go`, define an interface the concrete `*dlq.DLQService` already satisfies and use it as the field type:
```go
// dlqService is the behavior the handler needs; *dlq.DLQService satisfies it.
type DLQQuerier interface {
	QueryFailures(ctx context.Context, filter dlq.FailureFilter) ([]*dlq.FailedNotification, error)
	ReplayWorkflow(ctx context.Context, workflowID string) (*dlq.ReplayResult, error)
}

type DLQHandler struct {
	Service DLQQuerier
	logger  *slog.Logger
}

func NewDLQHandler(service DLQQuerier, logger *slog.Logger) *DLQHandler {
	return &DLQHandler{Service: service, logger: logger}
}
```
Add `"context"` to imports.

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: success — `cmd/server/server.go` passes a `*dlq.DLQService`, which satisfies `DLQQuerier`.

- [ ] **Step 3: Commit**

```bash
git add internal/api/dlq.go
git commit -m "refactor: depend DLQ handler on a service interface for testability"
```

### Task 1.3: Make the Temporal client constructor testable

**Files:**
- Modify: `utils/temporal_client.go`

- [ ] **Step 1: Use the error-returning option loader**

Replace the body so option-load errors are returned rather than panicking:
```go
package utils

import (
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
)

// NewTemporalClient creates a new Temporal client connection from environment
// configuration. The caller is responsible for calling Close() on the result.
func NewTemporalClient() (client.Client, error) {
	opts, err := envconfig.LoadDefaultClientOptions()
	if err != nil {
		return nil, err
	}
	return client.Dial(opts)
}
```
(If `envconfig.LoadDefaultClientOptions` is not present in this SDK version, keep `MustLoadDefaultClientOptions` wrapped in a deferred `recover` that converts the panic to an error — confirm by checking `go doc go.temporal.io/sdk/contrib/envconfig`.)

- [ ] **Step 2: Verify build**

Run: `go build ./... && go vet ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add utils/temporal_client.go
git commit -m "refactor: return error from NewTemporalClient option load"
```

### Task 1.4: Extract `cmd/*` logic into a testable `internal/app` package

Goal: move all branching logic out of the two `main()` functions so it can be unit-tested, leaving the mains as thin shells. This is the largest refactor; do it carefully and keep behavior identical.

**Files:**
- Create: `internal/app/server.go` (functions: `BuildServerMux`, `ParsePollInterval`)
- Create: `internal/app/worker.go` (function: `ResolveWorkerProvider`)
- Modify: `cmd/server/server.go` (use `app.BuildServerMux`, `app.ParsePollInterval`)
- Modify: `cmd/email_worker/email_worker.go` (use `app.ResolveWorkerProvider`, `app.ParsePollInterval`)

- [ ] **Step 1: Create `internal/app/server.go`**

```go
// Package app holds the wiring logic for the server and worker binaries,
// extracted from main() so it can be unit-tested.
package app

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"beacon/internal/api"
	"beacon/internal/config"
	"beacon/internal/dlq"
	"beacon/internal/notifier"
	"beacon/utils"

	"log/slog"
)

// ParsePollInterval returns the config poll interval from raw seconds, falling
// back to def when raw is empty or invalid.
func ParsePollInterval(raw string, def time.Duration) time.Duration {
	if raw == "" {
		return def
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return def
	}
	return time.Duration(secs) * time.Second
}

// ServerDeps are the dependencies needed to build the server mux.
type ServerDeps struct {
	TemporalClient api.WorkflowStarter
	Registry       *notifier.EmailClientRegistry
	ConfigService  *config.ConfigService
	Health         *config.HealthChecker
	DLQService     api.DLQQuerier // nil when Temporal is unavailable
	Logger         *slog.Logger
}

// BuildServerMux wires all HTTP routes. When DLQService is nil, the DLQ routes
// return 503 (Temporal unavailable).
func BuildServerMux(d ServerDeps) *http.ServeMux {
	email := &api.EmailHandler{TemporalClient: d.TemporalClient, Registry: d.Registry}
	adminHandler := api.NewAdminHandler(d.ConfigService, d.Registry, d.Logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/notify/email", email.HandleRequest)
	mux.HandleFunc("/healthz/live", d.Health.HandleLive)
	mux.HandleFunc("/healthz/ready", d.Health.HandleReady)
	mux.HandleFunc("/admin/config/refresh", adminHandler.HandleConfigRefresh)

	if d.DLQService != nil {
		dh := api.NewDLQHandler(d.DLQService, d.Logger)
		mux.HandleFunc("/dlq/failed", dh.HandleQueryFailures)
		mux.HandleFunc("/dlq/replay/", dh.HandleReplay)
	} else {
		unavailable := func(w http.ResponseWriter, r *http.Request) {
			utils.WriteError(w, http.StatusServiceUnavailable, "temporal service not available")
		}
		mux.HandleFunc("/dlq/failed", unavailable)
		mux.HandleFunc("/dlq/replay/", unavailable)
	}
	return mux
}

// Silence unused import if dlq is only referenced via interface in api.
var _ = dlq.NewDLQService
```
(Remove the trailing `var _ =` line if `dlq` ends up referenced elsewhere; it exists only to avoid an unused import while keeping the import for readers. Prefer simply not importing `dlq` here — adjust imports so the file compiles cleanly with no blank-identifier hacks.)

- [ ] **Step 2: Create `internal/app/worker.go`**

```go
package app

import (
	"fmt"

	"beacon/internal/config"
)

// ResolveWorkerProvider picks the SMTP provider this worker serves. If
// providerName is set it must exist in the bundle. Otherwise the is_default
// provider is used; if none is marked default and exactly one provider exists,
// that one is used. Returns the resolved name and config.
func ResolveWorkerProvider(bundle *config.ConfigBundle, providerName string) (string, *config.SMTPClientConfig, error) {
	if bundle == nil || len(bundle.SMTP) == 0 {
		return "", nil, fmt.Errorf("no SMTP providers in config")
	}
	if providerName != "" {
		cfg, ok := bundle.SMTP[providerName]
		if !ok {
			return "", nil, fmt.Errorf("%w: %s", config.ErrProviderNotFound, providerName)
		}
		return providerName, cfg, nil
	}
	for _, c := range bundle.SMTP {
		if c.IsDefault {
			return c.Name, c, nil
		}
	}
	if len(bundle.SMTP) == 1 {
		for _, c := range bundle.SMTP {
			return c.Name, c, nil
		}
	}
	return "", nil, fmt.Errorf("no provider resolved; set PROVIDER_NAME or mark one provider is_default")
}
```

- [ ] **Step 3: Rewrite `cmd/server/server.go` main to use the helpers**

Replace the mux-construction block (`internal/api` handler creation + `mux := http.NewServeMux()` ... DLQ wiring) with:
```go
	var dlqSvc api.DLQQuerier
	if temporalClient != nil {
		namespace := os.Getenv("TEMPORAL_NAMESPACE")
		if namespace == "" {
			namespace = "default"
		}
		dlqSvc = dlq.NewDLQService(temporalClient, namespace, logger)
	}

	healthChecker := confpkg.NewHealthChecker()
	healthChecker.SetReady(true)

	mux := app.BuildServerMux(app.ServerDeps{
		TemporalClient: temporalClient,
		Registry:       registry,
		ConfigService:  confpkg.GetConfigService(),
		Health:         healthChecker,
		DLQService:     dlqSvc,
		Logger:         logger,
	})
```
And replace the inline poll-interval parsing with `pollInterval := app.ParsePollInterval(os.Getenv("CONFIG_POLL_INTERVAL"), 300*time.Second)`. Add `"beacon/internal/app"` to imports; remove now-unused imports (`strconv` if no longer used). Keep signal handling, watcher start, and `ListenAndServe` in main.

Note: `temporalClient` is `client.Client`; assigning into `api.WorkflowStarter` and `dlq.NewDLQService(client.Client,...)` both still work.

- [ ] **Step 4: Rewrite `cmd/email_worker/email_worker.go` to use `ResolveWorkerProvider`**

Replace the provider-resolution block (lines that compute `smtpCfg`/`providerName`) with:
```go
	bundle := confpkg.GetConfigService().GetConfig()
	providerName, smtpCfg, err := app.ResolveWorkerProvider(bundle, os.Getenv("PROVIDER_NAME"))
	if err != nil {
		logger.Error("resolve worker provider", slog.Any("error", err))
		os.Exit(1)
	}
```
Replace inline poll parsing with `app.ParsePollInterval(...)` as above. Add `"beacon/internal/app"` import; drop `"strconv"` if unused.

- [ ] **Step 5: Verify build and existing tests**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: build/vet clean; existing config test still passes.

- [ ] **Step 6: Commit**

```bash
git add internal/app/ cmd/server/server.go cmd/email_worker/email_worker.go
git commit -m "refactor: extract server/worker wiring into testable internal/app"
```

### Task 1.5: Fix known doc/script drift

**Files:**
- Modify: `docs/DEVELOPMENT.md` (`make run-http` → `make run-server`)
- Modify: `scripts/test-local.sh` (`./cmd/http` → `./cmd/server`)

- [ ] **Step 1: Fix references**

Grep and correct: `grep -rn "run-http\|cmd/http" docs/ scripts/` then edit each occurrence to `run-server` / `cmd/server`.

- [ ] **Step 2: Sanity-check the script builds the right path**

Run: `bash -n scripts/test-local.sh` (syntax check) and confirm it now references `./cmd/server`.

- [ ] **Step 3: Commit**

```bash
git add docs/DEVELOPMENT.md scripts/test-local.sh
git commit -m "docs: fix run-server / cmd/server references"
```

---

## Phase 2 — `/simplify` pass

### Task 2.1: Simplify each package, keep tests green

- [ ] **Step 1:** For each package directory (`internal/api`, `internal/config`, `internal/dlq`, `internal/notifier`, `internal/temporal`, `internal/models`, `internal/app`, `utils`, `cmd/server`, `cmd/email_worker`), invoke the `/simplify` skill scoped to that package's diff/files. Apply only behavior-preserving simplifications (dedupe, dead code, clearer control flow).
- [ ] **Step 2:** After each package, run `go build ./... && go test ./...`. Both must stay green.
- [ ] **Step 3:** Commit per package: `git commit -am "refactor: simplify <package>"`.

> Note: `/simplify` reviews changed code; run it package-by-package so the diffs stay reviewable. Do not change public behavior or response shapes — Phase 5 verifies those by curl.

---

## Phase 3 — Coverage to ≥90% per package (TDD, parallelizable)

> Each task drives one package to ≥90%. Tasks 3.1–3.7 touch disjoint packages and can run in parallel agents (separate worktrees). The gate task 3.8 runs last on the merged result. For every package: write tests red→green, run `go test ./<pkg>/ -cover`, iterate until ≥90%, commit.

### Task 3.1: `utils` to ≥90%

**Files:**
- Create: `utils/http_response_test.go`
- Create: `utils/temporal_client_test.go`

- [ ] **Step 1: Test the response helpers**

`utils/http_response_test.go` — table-driven tests asserting status code, `Content-Type: application/json`, and decoded body for `WriteJSON`, `WriteSuccess`, `WriteError` using `httptest.NewRecorder()`. Example:
```go
func TestWriteSuccess(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteSuccess(rec, http.StatusAccepted, "ok", map[string]string{"k": "v"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: %q", ct)
	}
	var resp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Success || resp.Message != "ok" {
		t.Fatalf("resp: %+v", resp)
	}
}
```
Add equivalents for `WriteError` (Success false, Error set) and `WriteJSON` directly.

- [ ] **Step 2: Test `NewTemporalClient` (no server → fast connection error, no panic)**

`utils/temporal_client_test.go`:
```go
func TestNewTemporalClient_ConnectionError(t *testing.T) {
	t.Setenv("TEMPORAL_ADDRESS", "127.0.0.1:1") // nothing listening
	c, err := NewTemporalClient()
	if err == nil {
		c.Close()
		t.Skip("unexpected connection success in this environment")
	}
	// success criterion: it returned an error instead of panicking.
}
```
(If `client.Dial` blocks, set a short `TEMPORAL_DIAL_TIMEOUT` via envconfig or document pointing at `127.0.0.1:1` which yields immediate connection-refused. Confirm envconfig honors `TEMPORAL_ADDRESS`.)

- [ ] **Step 3: Run coverage**

Run: `go test ./utils/ -cover`
Expected: PASS, coverage ≥90%.

- [ ] **Step 4: Commit**

```bash
git add utils/
git commit -m "test: cover utils response helpers and temporal client"
```

### Task 3.2: `internal/models` and `internal/notifier` to ≥90%

**Files:**
- Create: `internal/notifier/email_test.go`
- Create: `internal/notifier/registry_test.go`

> `internal/models` is a plain struct with no statements; it needs no test and does not affect the gate (no executable lines). Focus on `notifier`.

- [ ] **Step 1: Test `EmailService.Send` happy path against the mock SMTP server**

`internal/notifier/email_test.go`:
```go
func TestEmailService_Send_Success(t *testing.T) {
	srv := testsupport.NewMockSMTPServer(t)
	svc := NewEmailService(srv.Host(), srv.Port(), "", "", "beacon@local", "Beacon")
	err := svc.Send(context.Background(), &Message[models.EmailMessage]{
		ID:   "1",
		Type: EmailNotifier,
		Data: models.EmailMessage{To: "a@b.com", Subject: "s", Body: "b"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := srv.Messages(); len(got) != 1 || got[0].To[0] != "a@b.com" {
		t.Fatalf("messages: %+v", got)
	}
}

func TestEmailService_Send_DialError(t *testing.T) {
	svc := NewEmailService("127.0.0.1", 1, "", "", "f@l", "F") // nothing listening
	err := svc.Send(context.Background(), &Message[models.EmailMessage]{
		Data: models.EmailMessage{To: "a@b.com", Subject: "s", Body: "b"},
	})
	if err == nil {
		t.Fatal("want dial error, got nil")
	}
}
```
Import `beacon/internal/testsupport` and `beacon/internal/models`.

- [ ] **Step 2: Test the registry resolution matrix**

`internal/notifier/registry_test.go` — cover `NewEmailClientRegistry` (nil/empty bundle error; auto-default single provider; explicit is_default), `Resolve` (known hint → provider; unknown hint → default; empty hint → default; no default + unknown hint → error; category mapped to missing provider → error by constructing a registry and mutating, or via Reload), `Reload` (replaces clients/categories/default; empty bundle error), `TaskQueueFor`, `ProviderNames` (sorted). Use a helper to build `*config.ConfigBundle`:
```go
func bundle(cfgs ...*config.SMTPClientConfig) *config.ConfigBundle {
	m := map[string]*config.SMTPClientConfig{}
	for _, c := range cfgs {
		m[c.Name] = c
	}
	return &config.ConfigBundle{SMTP: m, Revision: 1}
}
```
Example default-fallback test:
```go
func TestResolve_UnknownHintFallsBackToDefault(t *testing.T) {
	b := bundle(&config.SMTPClientConfig{Name: "p1", Categories: []string{"tx"}, IsDefault: true})
	r, err := NewEmailClientRegistry(b)
	if err != nil { t.Fatal(err) }
	_, name, err := r.Resolve("nope")
	if err != nil || name != "p1" {
		t.Fatalf("got name=%q err=%v", name, err)
	}
}
```

- [ ] **Step 3: Run coverage**

Run: `go test ./internal/notifier/ -cover`
Expected: PASS, ≥90%.

- [ ] **Step 4: Commit**

```bash
git add internal/notifier/
git commit -m "test: cover notifier email service and registry routing"
```

### Task 3.3: `internal/config` to ≥90%

**Files:**
- Create: `internal/config/validation_test.go`
- Create: `internal/config/service_infisical_test.go`
- Create: `internal/config/init_test.go`
- Create: `internal/config/watcher_test.go`
- Create: `internal/config/health_test.go`
- (existing `internal/config/service_test.go` stays)

- [ ] **Step 1: Validation coverage**

`validation_test.go` — table-driven over `ValidateConfig` covering: invalid JSON; each missing required field (name/provider/host/port/auth_type) via `validateStructural`; semantic failures (bad host, port out of range, bad auth_type, missing username for non-OAUTH2, missing password+api_key, TLS enabled without server_name, negative timeout/max_retries/max_per_hour); default timeout applied (30s) when zero; `isValidHost` for IP, localhost, valid DNS, invalid; `ValidationResult.Error()` formatting (valid → empty; invalid → joined). Provide valid base JSON and mutate per case.

- [ ] **Step 2: Infisical client coverage with httptest**

`service_infisical_test.go` — start `httptest.Server`s and construct via `NewConfigService(srv.URL, "proj", "prod", apiKey, clientID, clientSecret, logger)`. Cover:
  - `getAccessToken` client-secret path (server returns `{"accessToken":"t","expiresIn":3600}`), cached token reuse, api-key path (returns apiKey), token fallback, non-200 (4xx → error, 5xx → TransientError), decode error.
  - `fetchConfigs` success (returns `{"secrets":[{"secretKey":"p1","secretValue":"<json>"}]}`), 5xx transient, 4xx error, body decode error.
  - `loadFromInfisical` success → bundle with provider; validation error propagates.
  - `LoadWithRetry`: success first try; non-transient → fail fast; transient then success (override `backoffSchedule = []time.Duration{time.Millisecond}` in the test and restore via `t.Cleanup`); ctx cancelled.
  - `Store`/`GetConfig` (deep-copy), `GetClientConfig` (found / not-found / not-initialized), `RefreshConfig` (success; failure reverts to previous when present), `GetRevision`, `GetCacheAge` (nil current → -1).
Example transient-retry test:
```go
func TestLoadWithRetry_TransientThenSuccess(t *testing.T) {
	orig := backoffSchedule
	backoffSchedule = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { backoffSchedule = orig })

	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 { w.WriteHeader(500); return }
		w.Write([]byte(`{"secrets":[{"secretKey":"p1","secretValue":` +
			strconv.Quote(`{"name":"p1","provider":"x","host":"smtp.example.com","port":587,"auth_type":"PLAIN","username":"u","password":"p"}`) + `}]}`))
	}))
	defer srv.Close()
	cs := NewConfigService(srv.URL, "proj", "prod", "key", "", "", testLogger())
	b, err := cs.LoadWithRetry(context.Background())
	if err != nil { t.Fatalf("err: %v", err) }
	if _, ok := b.SMTP["p1"]; !ok { t.Fatalf("missing provider: %+v", b.SMTP) }
}
```

- [ ] **Step 3: Init + dev-bundle coverage**

`init_test.go` — `InitializeConfigService` dev path (`t.Setenv("DEV_MODE","true")`, `DEV_SMTP_HOST`...); `buildDevBundle` branches (missing host error, bad port error, defaults for name/auth/from/from_name, provider alias); `firstNonEmpty`; prod path pointed at an httptest Infisical server via `INFISICAL_ADDR`. Reset `globalConfigService` between tests if needed.

- [ ] **Step 4: Watcher coverage**

`watcher_test.go` — build a `ConfigService` backed by an httptest Infisical server that bumps providers; create `NewConfigWatcher(cs, time.Millisecond, onChange, logger)`; run `Start` in a goroutine with a context cancelled after the first `onChange` fires (use a channel). Assert `onChange` invoked with a bumped revision. Add a dev-mode case: a dev `ConfigService` (`RefreshConfig` returns `ErrDevModeSkip`) → `onChange` never called; cancel quickly.

- [ ] **Step 5: Health coverage**

`health_test.go` — `HandleLive` GET→200 "ok", non-GET→405 with Allow header; `HandleReady` not-ready→503, ready→200 "ready", non-GET→405; `SetError` setter.

- [ ] **Step 6: Run coverage**

Run: `go test ./internal/config/ -cover`
Expected: PASS, ≥90%.

- [ ] **Step 7: Commit**

```bash
git add internal/config/
git commit -m "test: cover config validation, infisical client, watcher, health, init"
```

### Task 3.4: `internal/temporal` to ≥90% (Temporal testsuite)

**Files:**
- Create: `internal/temporal/workflow_email_test.go`
- Create: `internal/temporal/activities_email_test.go`

- [ ] **Step 1: Activity test with a fake notifier**

`activities_email_test.go`:
```go
type fakeNotifier struct{ err error; got *notifier.Message[models.EmailMessage] }
func (f *fakeNotifier) Send(_ context.Context, m *notifier.Message[models.EmailMessage]) error {
	f.got = m
	return f.err
}

func TestSendEmailActivity_DelegatesToNotifier(t *testing.T) {
	fn := &fakeNotifier{}
	a := &EmailActivities{GetService: func() notifier.Notifier[models.EmailMessage] { return fn }}
	err := a.SendEmailActivity(context.Background(), &models.EmailMessage{To: "a@b.com", Subject: "s"})
	if err != nil { t.Fatalf("err: %v", err) }
	if fn.got == nil || fn.got.Data.To != "a@b.com" { t.Fatalf("not delegated: %+v", fn.got) }
}
```
Add a case where `fn.err != nil` and assert the error propagates.

- [ ] **Step 2: Workflow test with the test environment**

`workflow_email_test.go`:
```go
func TestSendEmailWorkflow_Success(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	fn := &fakeNotifier{}
	a := &EmailActivities{GetService: func() notifier.Notifier[models.EmailMessage] { return fn }}
	env.RegisterActivity(a.SendEmailActivity) // registers as "SendEmailActivity"

	env.ExecuteWorkflow(SendEmailWorkflow, &models.EmailMessage{To: "a@b.com", Subject: "s"})
	if !env.IsWorkflowCompleted() { t.Fatal("not completed") }
	if err := env.GetWorkflowError(); err != nil { t.Fatalf("workflow err: %v", err) }
}

func TestSendEmailWorkflow_RetriesThenFails(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	fn := &fakeNotifier{err: errors.New("smtp down")}
	a := &EmailActivities{GetService: func() notifier.Notifier[models.EmailMessage] { return fn }}
	env.RegisterActivity(a.SendEmailActivity)

	env.ExecuteWorkflow(SendEmailWorkflow, &models.EmailMessage{To: "a@b.com", Subject: "s"})
	if !env.IsWorkflowCompleted() { t.Fatal("not completed") }
	if env.GetWorkflowError() == nil { t.Fatal("want workflow error after retries") }
}
```
Import `go.temporal.io/sdk/testsuite`.

- [ ] **Step 3: Run coverage**

Run: `go test ./internal/temporal/ -cover`
Expected: PASS, ≥90%.

- [ ] **Step 4: Commit**

```bash
git add internal/temporal/
git commit -m "test: cover SendEmailWorkflow and SendEmailActivity via testsuite"
```

### Task 3.5: `internal/dlq` to ≥90% (Temporal SDK mocks)

**Files:**
- Create: `internal/dlq/query_test.go`
- Create: `internal/dlq/replay_test.go`

Use `go.temporal.io/sdk/mocks` (`mocks.Client`, `mocks.HistoryEventIterator`) to back a real `DLQService`. Build history events with `converter.GetDefaultDataConverter().ToPayloads(&models.EmailMessage{...})` for the WorkflowExecutionStarted input.

- [ ] **Step 1: Query tests**

`query_test.go` — cover `queryFailedWorkflows`/`statusMatches`/`workflowStatus`/`parseProviderFromTaskQueue`/`extractWorkflowDetails`:
  - `ListClosedWorkflow` returns executions with various statuses; assert status filtering (empty filter includes Failed/TimedOut/Canceled; specific filter is case-insensitive via `strings.EqualFold`).
  - provider filter via task-queue parsing (`email-sendgrid-queue` → `sendgrid`).
  - date defaults (from/to zero), limit clamping (>100 → 100, ≤0 → 20), offset beyond results → empty slice.
  - `extractWorkflowDetails`: iterator yields Started (with input payload), ActivityTaskFailed (bumps retryCount, sets failureReason), WorkflowExecutionFailed; assert recipient/subject/failureReason/retryCount.
  - `ListClosedWorkflow` error → propagated.
Provide a helper building `*workflowservice.ListClosedWorkflowExecutionsResponse` with `[]*workflowpb.WorkflowExecutionInfo`.

- [ ] **Step 2: Replay tests**

`replay_test.go` — cover `replayWorkflow`:
  - `DescribeWorkflowExecution` error → `ErrWorkflowNotFound`.
  - non-terminal status → `ErrNotTerminalState`.
  - terminal status + history with input → `ExecuteWorkflow` called with `replay-<id>` and `ALLOW_DUPLICATE_FAILED_ONLY`; returns `ReplayResult`.
  - `ExecuteWorkflow` returns already-started error → `ErrReplayAlreadyRunning` (use `serviceerror.NewWorkflowExecutionAlreadyStarted(...)` so `temporalerr.IsWorkflowExecutionAlreadyStartedError` matches).
  - history without input payload → "input not found" error.
Example skeleton:
```go
func TestReplay_NotTerminal(t *testing.T) {
	mc := &mocks.Client{}
	mc.On("DescribeWorkflowExecution", mock.Anything, "wf1", "").
		Return(describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, "email-p1-queue"), nil)
	_, err := replayWorkflow(context.Background(), mc, "wf1")
	if !errors.Is(err, ErrNotTerminalState) { t.Fatalf("got %v", err) }
}
```

- [ ] **Step 3: Run coverage**

Run: `go test ./internal/dlq/ -cover`
Expected: PASS, ≥90%.

- [ ] **Step 4: Commit**

```bash
git add internal/dlq/
git commit -m "test: cover DLQ query and replay via temporal mocks"
```

### Task 3.6: `internal/api` to ≥90%

**Files:**
- Create: `internal/api/email_test.go`
- Create: `internal/api/admin_test.go`
- Create: `internal/api/dlq_test.go`

- [ ] **Step 1: Email handler tests**

`email_test.go` — fake `WorkflowStarter` + a registry from a config bundle; drive via `httptest`:
```go
type fakeStarter struct{ run client.WorkflowRun; err error; called bool }
func (f *fakeStarter) ExecuteWorkflow(_ context.Context, _ client.StartWorkflowOptions, _ interface{}, _ ...interface{}) (client.WorkflowRun, error) {
	f.called = true
	return f.run, f.err
}

type fakeRun struct{}
func (fakeRun) GetID() string { return "wf-1" }
func (fakeRun) GetRunID() string { return "run-1" }
func (fakeRun) Get(context.Context, interface{}) error { return nil }
func (fakeRun) GetWithOptions(context.Context, interface{}, client.WorkflowRunGetOptions) error { return nil }
```
Cases: method != POST → 405; nil TemporalClient → 503; bad JSON body → 400; missing `to` → 400; missing `subject` → 400; invalid email → 400; routing error (registry with no default + unknown hint) → 400; ExecuteWorkflow error → 500; success → 202 with `workflow_id`/`workflow_run_id`/`provider`. Build the registry with `notifier.NewEmailClientRegistry(bundle)`.

- [ ] **Step 2: Admin handler tests**

`admin_test.go` — cover `HandleConfigRefresh`: non-POST → 405; `ADMIN_TOKEN` unset → 403; wrong token → 401; dev-mode refresh (`ErrDevModeSkip`) → 503; success path → 200 with revision + providers. For success use a dev `ConfigService`? `RefreshConfig` in dev returns `ErrDevModeSkip` → 503; to get 200 you need a non-dev service whose `RefreshConfig` succeeds — back it with an httptest Infisical server (reuse helper from config tests via a small local fake) OR construct a `ConfigService` via `config.NewConfigService(srv.URL,...)`, `Store` an initial bundle, then call. Use `t.Setenv("ADMIN_TOKEN","secret")` and header `Authorization: Bearer secret`. Registry reload uses the bundle.

- [ ] **Step 3: DLQ handler tests with a fake `DLQQuerier`**

`dlq_test.go`:
```go
type fakeDLQ struct {
	failures []*dlq.FailedNotification
	qErr     error
	replay   *dlq.ReplayResult
	rErr     error
}
func (f *fakeDLQ) QueryFailures(context.Context, dlq.FailureFilter) ([]*dlq.FailedNotification, error) {
	return f.failures, f.qErr
}
func (f *fakeDLQ) ReplayWorkflow(context.Context, string) (*dlq.ReplayResult, error) {
	return f.replay, f.rErr
}
```
Cases for `HandleQueryFailures`: non-GET → 405; bad `from`/`to` → 400; limit>100 clamped (assert via captured filter — capture by recording in the fake); query error → 500; success → 200 with `count`. For `HandleReplay`: non-POST → 405; empty workflow ID (`/dlq/replay/`) → 400; `ErrWorkflowNotFound` → 404; `ErrNotTerminalState` → 409; `ErrReplayAlreadyRunning` → 409; other error → 500; success → 202.

- [ ] **Step 4: Run coverage**

Run: `go test ./internal/api/ -cover`
Expected: PASS, ≥90%.

- [ ] **Step 5: Commit**

```bash
git add internal/api/
git commit -m "test: cover email, admin, and DLQ HTTP handlers"
```

### Task 3.7: `internal/app` to ≥90%

**Files:**
- Create: `internal/app/server_test.go`
- Create: `internal/app/worker_test.go`

- [ ] **Step 1: `ParsePollInterval` + `BuildServerMux` routing**

`server_test.go` — `ParsePollInterval`: empty → default; valid → seconds; invalid/≤0 → default. `BuildServerMux`: build with a fake `WorkflowStarter`, a registry, a dev `ConfigService`, a `HealthChecker`, and (a) `DLQService: nil` → GET `/dlq/failed` returns 503; (b) a fake `DLQQuerier` → `/dlq/failed` returns 200. Drive routes via `httptest.NewServer(mux)` or `mux.ServeHTTP(rec, req)` and assert status codes for `/healthz/live`, `/notify/email` (405 on GET), etc.

- [ ] **Step 2: `ResolveWorkerProvider`**

`worker_test.go` — cases: nil/empty bundle → error; explicit provider found; explicit provider missing → error wrapping `ErrProviderNotFound`; is_default chosen; single provider auto-selected; multiple providers without default → error.

- [ ] **Step 3: Run coverage**

Run: `go test ./internal/app/ -cover`
Expected: PASS, ≥90%.

- [ ] **Step 4: Commit**

```bash
git add internal/app/
git commit -m "test: cover internal/app server and worker wiring"
```

### Task 3.8: Run the coverage gate (merge point)

- [ ] **Step 1:** After all worktrees merge, run `make cover`.
  Expected: `Coverage gate PASSED.` If any package is below 90%, add targeted tests for the uncovered lines (`make cover-html` then open `coverage.html` to find red lines) and re-run.
- [ ] **Step 2:** Commit any gap-filling tests: `git commit -am "test: close remaining coverage gaps to pass 90% gate"`.

---

## Phase 4 — Integration tests (TDD, mocked downstream services)

### Task 4.1: End-to-end integration harness

**Files:**
- Create: `internal/integration/integration_test.go`

The harness wires the **real** `EmailHandler` to a real Temporal client against `go.temporal.io/sdk/testsuite`'s test server is not suitable for HTTP→client flow; instead use the **dev server** when available, else skip. Strategy: detect `TEMPORAL_ADDRESS` reachable; if not, `t.Skip`. The mocked downstream service is a plain HTTP client; the mock SMTP server (Task 0.2) captures delivery.

- [ ] **Step 1: Write the harness + happy-path test (red)**

```go
//go:build integration

package integration

// Build/run with: go test -tags=integration ./internal/integration/...
// Requires a reachable Temporal (TEMPORAL_ADDRESS, default localhost:7233) and
// starts an in-process worker pointed at the mock SMTP server.
```
The test: start mock SMTP; build a dev `ConfigBundle` with one provider pointing at the mock SMTP host/port and `is_default`; start an in-process Temporal worker (`worker.New`) on `TaskQueueFor("dev")` registering `SendEmailWorkflow` + an `EmailActivities` whose `GetService` returns a real `EmailService` for the mock SMTP; stand up the real `EmailHandler` via `httptest.NewServer`; POST a valid email as the "downstream service"; poll the mock SMTP until the message arrives or timeout; assert recipient/subject/body.

- [ ] **Step 2: Run (red, then green once implemented)**

Run: `go test -tags=integration ./internal/integration/ -run Happy -v`
Expected: PASS when Temporal is reachable; SKIP otherwise.

- [ ] **Step 3: Add scenario tests**

Add tests (same build tag) for: routing by `client_hint` (two providers, two mock SMTP servers, assert the right one receives); validation failure (`400`, no SMTP delivery); method not allowed (`405`); SMTP failure → retries exhausted → workflow terminal → `/dlq/failed` lists it → `/dlq/replay/{id}` re-dispatches (point the replay provider at a now-healthy mock SMTP and assert delivery); `/admin/config/refresh` changes routing.

- [ ] **Step 4: Commit**

```bash
git add internal/integration/
git commit -m "test: end-to-end integration tests with mocked downstream + SMTP"
```

### Task 4.2: Make integration tests runnable in the Makefile

- [ ] **Step 1:** Add to `Makefile`:
```makefile
.PHONY: test-integration
test-integration:
	go test -tags=integration ./internal/integration/...
```
- [ ] **Step 2:** Commit: `git commit -am "chore: add test-integration make target"`.

---

## Phase 5 — Local deploy + feature-readiness matrix

### Task 5.1: Local full-stack compose (dev) + fix mock Infisical path

**Files:**
- Create: `deploy/docker-compose.local.yml`
- Modify: `scripts/mock-infisical.go` (confirm `/api/v4/secrets` path; already fixed per commit d63e9af — verify)
- Create: `deploy/env/.env.local` (DEV_MODE stack)

- [ ] **Step 1:** Author `deploy/docker-compose.local.yml` bringing up: `temporal` (dev server image `temporalio/auto-setup` or `temporalio/temporal` CLI dev) + `postgresql` + `temporal-ui`, `mailpit` (SMTP sink + web UI), Beacon `server`, Beacon `email_worker` (DEV_MODE pointing SMTP at mailpit). Reuse the Phase 6 Dockerfile (Task 6.1) for the Beacon images; if Phase 6 is not yet merged, build locally with `go build`.
- [ ] **Step 2:** Bring it up: `docker compose -f deploy/docker-compose.local.yml up -d`; wait for `/healthz/ready` to return 200.
- [ ] **Step 3:** Commit: `git add deploy/ && git commit -m "chore: local full-stack compose for readiness checks"`.

> If Docker is unavailable in the execution environment, fall back to: `temporal server start-dev` + `go run ./scripts/mock-infisical.go` (or DEV_MODE) + mailpit binary + `make run-server` + `make run-email-worker`. Record which path was used in the readiness doc.

### Task 5.2: Curl every endpoint → `docs/FEATURE_READINESS.md`

**Files:**
- Create: `docs/FEATURE_READINESS.md`
- Create: `scripts/readiness-check.sh`

- [ ] **Step 1:** Write `scripts/readiness-check.sh` that curls each endpoint and prints request + response + status:
  - `GET /healthz/live` → 200 `ok`
  - `GET /healthz/ready` → 200 `ready`
  - `POST /notify/email` valid → 202 (+ capture workflow_id); invalid email → 400; missing subject → 400; wrong method GET → 405
  - `POST /admin/config/refresh` without token → 401/403; with `ADMIN_TOKEN` → 200 (or 503 in DEV_MODE — record actual)
  - `GET /dlq/failed` → 200 with `count`; bad `from` date → 400
  - `POST /dlq/replay/<id>` for a known failed workflow → 202; unknown → 404
- [ ] **Step 2:** Run it against the local stack; paste actual request/response/status into `docs/FEATURE_READINESS.md` as a table with a Pass/Fail column and notes (including the DEV_MODE admin-refresh 503 behavior). Verify email arrives in mailpit.
- [ ] **Step 3:** If any endpoint deviates from `docs/API.md`, note it and reconcile `docs/API.md`.
- [ ] **Step 4:** Commit: `git add docs/FEATURE_READINESS.md scripts/readiness-check.sh && git commit -m "docs: feature-readiness matrix from local deploy"`.

### Task 5.3 (optional, strengthens the gate): instrumented cmd coverage

- [ ] **Step 1:** Build instrumented binaries: `go build -cover -o bin/server ./cmd/server` and `... ./cmd/email_worker`.
- [ ] **Step 2:** Run them under `GOCOVERDIR=covdata` during the readiness checks; after shutdown, `go tool covdata percent -i=covdata` to record `cmd/*` coverage in `docs/FEATURE_READINESS.md` (demonstrates the main shells are exercised end-to-end).
- [ ] **Step 3:** Commit the recorded numbers (not the covdata dir, which is gitignored).

---

## Phase 6 — Deployment artifacts + runbook

### Task 6.1: Multi-stage Dockerfile (server + worker)

**Files:**
- Create: `Dockerfile`
- Create: `.dockerignore`

- [ ] **Step 1:** Author a multi-stage build producing both binaries from one image, selectable by command:
```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -o /out/email_worker ./cmd/email_worker

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /usr/local/bin/server
COPY --from=build /out/email_worker /usr/local/bin/email_worker
USER nonroot:nonroot
# Default to the server; override `command` for the worker.
ENTRYPOINT ["/usr/local/bin/server"]
```
`.dockerignore`: `bin/`, `covdata/`, `*.out`, `.git`, `aidlc-docs/`, `docs/`.

- [ ] **Step 2:** Validate locally: `docker build -t beacon:local .` and `docker run --rm beacon:local --help` (or confirm it starts and fails fast without Temporal). Record success.
- [ ] **Step 3:** Commit: `git add Dockerfile .dockerignore && git commit -m "build: multi-stage Dockerfile for server and worker"`.

### Task 6.2: Production docker-compose (Temporal cluster + workers + tunnel)

**Files:**
- Create: `deploy/docker-compose.yml`
- Create: `deploy/dynamicconfig/development-sql.yaml` (Temporal dynamic config)

- [ ] **Step 1:** Author `deploy/docker-compose.yml` with services:
  - `postgresql` (Temporal persistence; named volume for durability)
  - `temporal` (`temporalio/auto-setup`) wired to Postgres, `temporal-ui`
  - `beacon-server` (image from Task 6.1, env from Infisical machine identity, `TEMPORAL_ADDRESS=temporal:7233`)
  - one `beacon-worker-<provider>` per provider (set `PROVIDER_NAME`), scalable
  - `cloudflared` (tunnel; see Task 6.3 config) with `command: tunnel run`
  Include healthchecks (server `/healthz/ready`), `restart: unless-stopped`, and a named Postgres volume.
- [ ] **Step 2:** Validate compose syntax: `docker compose -f deploy/docker-compose.yml config`.
- [ ] **Step 3:** Commit: `git add deploy/ && git commit -m "deploy: production compose for Temporal cluster, workers, tunnel"`.

### Task 6.3: Cloudflare Tunnel config, systemd units, env templates

**Files:**
- Create: `deploy/cloudflared/config.yml`
- Create: `deploy/systemd/beacon-server.service`, `deploy/systemd/beacon-worker@.service`, `deploy/systemd/cloudflared.service`
- Create: `deploy/env/server.env.example`, `deploy/env/worker.env.example`

- [ ] **Step 1:** `deploy/cloudflared/config.yml` ingress exposes **only health publicly**; gates the rest:
```yaml
tunnel: <TUNNEL_ID>
credentials-file: /etc/cloudflared/<TUNNEL_ID>.json
ingress:
  - hostname: beacon.example.com
    path: /healthz/*
    service: http://beacon-server:6969
  # /notify/email and /dlq/* require Cloudflare Access (service tokens) —
  # configure an Access application over these paths in the Zero Trust dashboard.
  - hostname: beacon.example.com
    path: /notify/email
    service: http://beacon-server:6969
  - hostname: beacon.example.com
    path: /dlq/*
    service: http://beacon-server:6969
  - service: http_status:404
```
Add a comment block: without app-layer auth, `/notify/email` is an open relay unless protected by Cloudflare Access; reference `docs/future-scope.md`.
- [ ] **Step 2:** systemd units: `beacon-server.service` (ExecStart server binary, EnvironmentFile=server.env), templated `beacon-worker@.service` (`%i` = provider name → `PROVIDER_NAME=%i`), `cloudflared.service`. Include `Restart=always`, `After=network-online.target`.
- [ ] **Step 3:** env templates listing every variable from `docs/CONFIGURATION.md` with placeholders (TEMPORAL_ADDRESS, TEMPORAL_NAMESPACE, INFISICAL_*, ADMIN_TOKEN, CONFIG_POLL_INTERVAL, PROVIDER_NAME, SERVER_PORT, DEV_MODE=false).
- [ ] **Step 4:** Commit: `git add deploy/ && git commit -m "deploy: cloudflared ingress, systemd units, env templates"`.

### Task 6.4: Deployment runbook

**Files:**
- Create: `docs/DEPLOYMENT.md`

- [ ] **Step 1:** Write the runbook with these sections, each with exact commands:
  1. Overview + topology diagram (server, workers, Temporal+Postgres+UI, cloudflared).
  2. Prerequisites (Docker/Compose or systemd host, a Cloudflare account + `cloudflared`, an Infisical instance/project).
  3. Temporal cluster bring-up (compose up; create/verify namespace with `temporal operator namespace`).
  4. Infisical setup: `/beacon/smtp` provider entries (link `infisical-example.json`); machine-identity credentials.
  5. Beacon server + per-provider workers (compose scale or systemd `systemctl enable --now beacon-worker@sendgrid`).
  6. Cloudflare Tunnel: `cloudflared tunnel create beacon`, DNS route, drop credentials, apply `config.yml`, create the Access application protecting `/notify/email` and `/dlq/*` (service tokens for downstream services).
  7. **Security note:** no app-layer auth yet → open-relay risk; trusted-network/Access assumption; link `docs/future-scope.md`.
  8. Observability: Temporal UI, health probes, logs.
  9. Upgrade/rollback (image tags), Postgres backup/restore, scaling workers.
- [ ] **Step 2:** Commit: `git add docs/DEPLOYMENT.md && git commit -m "docs: home-server deployment runbook"`.

---

## Phase 7 — Integration guide, future scope, doc reconciliation

### Task 7.1: Downstream integration guide

**Files:**
- Create: `docs/INTEGRATION.md`

- [ ] **Step 1:** Write the guide:
  1. What Beacon is and the async `202` contract.
  2. Beacon-side setup (operator): add/confirm the SMTP provider + its `categories` in Infisical `/beacon/smtp`; agree the `client_hint` the downstream will send; how default routing works.
  3. Endpoint, method, headers, request schema (`to`, `subject`, `body`, `client_hint`), validation rules.
  4. Response shape + status codes (202/400/405/503/500); how to read `workflow_id`.
  5. Async semantics: retries (5s→2m, 3 attempts), DLQ visibility, replay.
  6. Examples: curl, Go (`net/http`), Python (`requests`).
  7. Note: per-service auth is planned — link `docs/future-scope.md`.
- [ ] **Step 2:** Verify every curl example against the local stack (reuse Phase 5) so examples are real.
- [ ] **Step 3:** Commit: `git add docs/INTEGRATION.md && git commit -m "docs: downstream integration guide"`.

### Task 7.2: Future-scope auth design

**Files:**
- Create: `docs/future-scope.md`

- [ ] **Step 1:** Document (do not implement) per-service API-key auth, lifted from the spec §9: `internal/auth` package; `/beacon/api-keys` (prod) / `DEV_API_KEYS` (dev) loaded by `ConfigService` + hot-reload; per-service `{name, key_hash(sha256-hex), allowed_categories[], enabled}`; `Authorization: Bearer <key>` + constant-time compare; category-scope enforcement (403) and missing/invalid key (401); `API_AUTH_ENABLED` toggle; protect `/dlq/*`; `make genkey` helper; migration plan (enable internal-first, register services, then expose publicly behind Access + app auth).
- [ ] **Step 2:** Commit: `git add docs/future-scope.md && git commit -m "docs: future-scope per-service API-key auth design"`.

### Task 7.3: Reconcile existing docs

**Files:**
- Modify: `docs/API.md`, `docs/ARCHITECTURE.md`, `docs/CONFIGURATION.md`, `docs/DEVELOPMENT.md`, `README.md`

- [ ] **Step 1:** Update `docs/API.md` to include `/admin/config/refresh`, `/dlq/failed`, `/dlq/replay/{id}` (they exist in code but the doc may only list notify+health), with the verified I/O from `docs/FEATURE_READINESS.md`.
- [ ] **Step 2:** Add links from `README.md` to DEPLOYMENT, INTEGRATION, FEATURE_READINESS, future-scope. Ensure `make` targets and run commands are correct.
- [ ] **Step 3:** Commit: `git commit -am "docs: reconcile API/README with verified behavior"`.

---

## Orchestration Appendix (agent teams)

- **Team:** create via `TeamCreate`. A coordinator (this session) owns merges, the gate, and final verification.
- **Phase 0–1 (sequential):** one agent on a shared branch establishes tooling + refactors; everyone branches from the resulting commit.
- **Phase 2 (`/simplify`):** one agent (sequential, package-by-package) to keep diffs reviewable.
- **Phase 3 (parallel):** spawn agents 3.1–3.7 in **separate git worktrees** (use superpowers:using-git-worktrees), one package each. They touch disjoint files; the only shared file is `go.mod`/`go.sum` — the coordinator runs `go mod tidy` once after merge. Coordinator runs Task 3.8 gate.
- **Phase 4:** 1 agent.
- **Phase 5:** 1 agent (needs Docker or the dev-server fallback).
- **Phase 6–7 (parallel):** deployment-artifacts agent, integration-guide agent, future-scope agent — disjoint files.
- **Merge discipline:** each agent commits frequently; coordinator merges in dependency order, runs `go build ./... && go test ./... && make cover` after each merge.

---

## Self-Review

- **Spec coverage:** §4.1 simplify → Phase 2; §4.2 coverage gate → Phase 0 + Phase 3; §4.3 integration → Phase 4; §4.4 readiness → Phase 5; §4.5 deploy artifacts/runbook → Phase 6; §4.6 integration guide → Phase 7.1; §9 future scope → Phase 7.2; doc drift → Phase 1.5/7.3. All covered.
- **Type consistency:** `WorkflowStarter`, `DLQQuerier`, `ServerDeps`, `BuildServerMux`, `ParsePollInterval`, `ResolveWorkerProvider`, `MockSMTPServer`/`CapturedMessage`, `fakeNotifier`/`fakeStarter`/`fakeRun`/`fakeDLQ` are named consistently across tasks. Handler field renames (`TemporalClient WorkflowStarter`, `Service DLQQuerier`) are reflected in `internal/app` wiring and `cmd/server`.
- **Placeholders:** none — representative full tests provided per package; exhaustive case lists given where tests are mechanical; deployment artifacts have full content.
- **Known risks flagged inline:** `envconfig.LoadDefaultClientOptions` availability (Task 1.3), `client.Dial` timeout behavior (Task 3.1), Docker availability fallback (Task 5.1).
