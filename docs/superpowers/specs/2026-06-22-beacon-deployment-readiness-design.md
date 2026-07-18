# Beacon — Production-Readiness, Deployment & Integration Design

**Date:** 2026-06-22
**Status:** Approved (brainstorming) — pending implementation plan
**Owner:** Beacon maintainer

## 1. Goal

Make Beacon production-deployable on a home server and demonstrably feature-complete, by delivering four tightly-coupled outcomes:

1. A complete **deployment runbook + ready-to-run artifacts** for a home server: a self-hosted Temporal cluster (Docker Compose + Postgres), Beacon API server, per-provider Temporal workers, and public exposure via a Cloudflare Tunnel.
2. A **downstream-service integration guide** covering how an external service sends mail through Beacon, including the Beacon-side configuration needed for that service.
3. **Feature-readiness verification**: deploy locally, exercise every HTTP endpoint with real curl requests, capture actual I/O, and run a `/simplify` pass over all packages — with **90%+ test coverage on all code paths (hard gate)**.
4. **Integration tests** with mocked downstream services and mocked SMTP, written via TDD.

Authentication is explicitly **out of scope** for this effort and is captured as future work (Section 9).

## 2. Background — current state

Beacon is an asynchronous email notification service in Go (1.24.4). Two binaries:

- `cmd/server/server.go` — HTTP API on `SERVER_PORT` (default `6969`).
- `cmd/email_worker/email_worker.go` — Temporal worker; consumes `email-<provider>-queue`.

Key packages: `internal/api` (handlers), `internal/config` (Infisical/dev config, watcher, health), `internal/dlq` (query + replay), `internal/notifier` (SMTP via `gopkg.in/mail.v2`, multi-provider registry), `internal/temporal` (workflow + activity), `internal/models`, `utils`.

**HTTP surface:**

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/notify/email` | Enqueue an email (returns `202` + workflow IDs) |
| GET | `/healthz/live` | Liveness |
| GET | `/healthz/ready` | Readiness |
| POST | `/admin/config/refresh` | Reload config (Bearer `ADMIN_TOKEN`) |
| GET | `/dlq/failed` | Query failed/timed-out/canceled workflows |
| POST | `/dlq/replay/{workflowID}` | Replay a terminal workflow |

**Config:** `DEV_MODE=true` loads a single SMTP provider from `DEV_SMTP_*` env vars; otherwise config is fetched from Infisical at `/beacon/smtp` (machine-identity, API-key, or legacy-token auth), validated, cached, and hot-reloaded by `ConfigWatcher` (poll interval `CONFIG_POLL_INTERVAL`, default 300s) and by `POST /admin/config/refresh`.

**Routing:** `client_hint` → category → provider via `EmailClientRegistry.Resolve`; task queue is `email-<provider>-queue`. A provider may be marked `is_default`.

**Gaps today:** essentially one test (`internal/config/service_test.go`); no Dockerfile / compose; no deployment docs; no CI; no API authentication on `/notify/email` or `/dlq/*`. Known doc/script drift: `docs/DEVELOPMENT.md` references `make run-http` (should be `make run-server`); `scripts/test-local.sh` references `./cmd/http` (should be `./cmd/server`).

## 3. Non-goals

- No application-layer authentication implemented in this effort (documented as future scope).
- No Kubernetes manifests (Docker Compose + systemd only).
- No new email providers or vendor SDKs (SMTP only, as today).
- No changes to the public request/response contract of existing endpoints beyond what `/simplify` and bug-fixes require (any contract change must be called out and reflected in docs + tests).

## 4. Workstreams

### 4.1 `/simplify` pass (all packages)

Run the `/simplify` skill across `internal/api`, `internal/config`, `internal/dlq`, `internal/notifier`, `internal/temporal`, `internal/models`, `utils`, and `cmd/*`. Reduce duplication and complexity with **no behavior change**; all tests must remain green. Fix the known doc/script drift (`run-http`→`run-server`, `cmd/http`→`cmd/server`) as part of this pass.

### 4.2 Coverage to 90%+ (hard gate, TDD)

- **Gate:** `make cover` runs `go test -coverprofile` per package; `scripts/check-coverage.sh` parses per-package coverage and **exits non-zero if any package is below 90%**. This is a blocking requirement.
- **Testability refactors:** thin `main()` functions delegate to a testable `run(ctx, deps)`; introduce interfaces for the Temporal client, the notifier (SMTP send), and config access so they can be substituted in tests. Refactors must not change runtime behavior.
- **Per-domain approach:**
  - `internal/api`: `net/http/httptest`; table-driven tests for every status code path (200/202/400/401-as-applicable/403-as-applicable/404/405/409/500/503).
  - `internal/temporal`: `go.temporal.io/sdk/testsuite` for `SendEmailWorkflow` / `SendEmailActivity`, including retry-policy behavior.
  - `internal/notifier`: fake SMTP via injected interface; registry resolution + category mapping + default fallback.
  - `internal/config`: validation (structural + semantic), dev-bundle build, watcher tick/reload, Infisical client paths (mocked HTTP), health checker.
  - `internal/dlq`: query filtering (status/provider/date/limit/offset) and replay idempotency with a mocked Temporal client.
  - `utils`: response helpers, temporal client construction (where feasible).
- **Libraries:** standard `testing` + `httptest`; `testify` (already in module graph) for assertions; a minimal in-process SMTP sink for capture (lightweight library or hand-rolled).

### 4.3 Integration tests (TDD, mocked downstream services)

- A **mocked downstream service** is a test HTTP client that issues real requests to an in-process Beacon server (`httptest`). No API key (auth out of scope).
- **End-to-end path under test:** request → Temporal (`testsuite` env, or a dockerized dev server for the full-stack variant) → `SendEmailActivity` → **mock SMTP server** capturing the delivered message → assertions on recipient/subject/body and provider routing.
- **Scenarios:** happy path per category; provider routing via `client_hint` (known hint, unknown hint → default, no hint → default); validation failures (`400`); method enforcement (`405`); SMTP failure → retries exhausted → workflow visible in DLQ; `/admin/config/refresh` reload changes routing; `/dlq/failed` filters; `/dlq/replay/{id}` happy path + `404`/`409`.
- Written red→green→refactor per `/test-driven-development`.

### 4.4 Feature-readiness verification (local deploy + curl matrix)

Bring up the full stack locally (Docker Compose: Temporal + Postgres + Temporal UI + Beacon server + worker + mock Infisical + mock SMTP/mailpit). Then `curl` **every endpoint** with required inputs and record actual request, response body, and status code into `docs/FEATURE_READINESS.md` as a pass/fail matrix covering: `/notify/email` (each category + validation failures), `/healthz/live`, `/healthz/ready`, `/admin/config/refresh` (authorized + unauthorized), `/dlq/failed` (filters), `/dlq/replay/{id}`.

### 4.5 Deployment runbook + artifacts

**Artifacts (committed, locally validated):**
- Multi-stage `Dockerfile` building both `server` and `email_worker`.
- `docker-compose.yml` (and/or `deploy/` directory): Temporal auto-setup + Postgres + Temporal UI, Beacon server, one worker per provider (parameterized by `PROVIDER_NAME`), `cloudflared`, optional mailpit for staging.
- `cloudflared` `config.yml`: ingress exposing **only `/healthz/*` publicly**; `/notify/email` and `/dlq/*` placed behind **Cloudflare Access (service tokens / Zero Trust)** or kept off the public ingress.
- `systemd` unit files as a non-Docker alternative (cloudflared + server + worker).
- `.env` / secret templates.

**Runbook (`docs/DEPLOYMENT.md`):** prerequisites; Temporal cluster bring-up + namespace creation; scaling workers per provider; Infisical setup for `/beacon/smtp`; Cloudflare Tunnel creation + DNS + ingress + Access policy; secrets handling; upgrade/rollback; backup of Temporal Postgres; observability (Temporal UI, health probes). The runbook states the **open-relay risk** plainly given no app-layer auth, documents the trusted-network/Zero-Trust assumption, and links `docs/future-scope.md`.

### 4.6 Downstream integration guide (`docs/INTEGRATION.md`)

End-to-end for a downstream team: network access to Beacon → **Beacon-side setup** (operator ensures the desired SMTP provider/category exists in `/beacon/smtp`; agree on the `client_hint` the service will send) → endpoint, request schema, `client_hint`→category routing, `202` response semantics, error handling, async/retry/DLQ behavior, and copy-paste examples in curl / Go / Python. A note flags that per-service auth is planned (links `docs/future-scope.md`).

## 5. Architecture / data flow (unchanged at runtime)

```
downstream service --HTTP POST /notify/email--> Beacon server
  Beacon server --StartWorkflow(email-<provider>-queue)--> Temporal cluster
  Temporal --task--> email_worker (per provider) --SMTP--> provider --> recipient
  failures --> terminal workflow --> queryable/replayable via /dlq/*
```

Deployment topology adds: Postgres (Temporal persistence), Temporal UI, cloudflared (public edge), and one worker process per provider task queue.

## 6. Error handling

Preserve existing status-code semantics (validated by the curl matrix and tests). The coverage work must exercise each documented error branch rather than change it. Any behavior correction discovered during `/simplify` or testing is documented in `docs/FEATURE_READINESS.md` and reflected in `docs/API.md`.

## 7. Testing strategy summary

- **Unit tests** per package → drive the 90% hard gate.
- **Integration tests** → end-to-end through the HTTP handler + Temporal + mock SMTP, simulating downstream services (TDD).
- **Manual/local readiness** → curl matrix against the full Docker Compose stack.
- Coverage enforced in `make cover` / `scripts/check-coverage.sh`; intended for CI later (CI itself out of scope unless trivial).

## 8. Orchestration (agent teams)

Execute Approach A with a spawned team:

1. `/simplify` + doc/script drift fixes (1 agent) — establishes a clean baseline.
2. **Parallel coverage fan-out** — one agent per package (`api`, `config`, `dlq`, `notifier`, `temporal`, `utils`/`models`/`cmd`), each in an **isolated git worktree** to avoid `go.mod`/branch conflicts; a coordinator merges and runs the coverage gate.
3. Integration tests (1–2 agents).
4. Local deploy + readiness matrix (1 agent).
5. **Parallel docs** — deployment runbook, integration guide, future-scope (separate agents).

A coordinator owns merges, the coverage gate, and final verification.

## 9. Future scope — `docs/future-scope.md`

Document (not implement) per-service API-key authentication:

- New `internal/auth` package with an `Authenticator` validating inbound `/notify/email` (and `/dlq/*`) requests.
- Keys stored like SMTP config: Infisical `/beacon/api-keys` (prod) / `DEV_API_KEYS` JSON (dev); hot-reloaded via `ConfigWatcher` + `/admin/config/refresh`.
- Per-service entry: `{ name, key_hash (sha256-hex), allowed_categories[], enabled }`. Beacon stores only the hash; operator hands plaintext to the downstream service. `make genkey` helper emits key + hash.
- `Authorization: Bearer <api-key>`, constant-time compare (`crypto/subtle`); the service's `allowed_categories` must include the resolved category (empty hint → default provider's category) else `403`; missing/invalid key → `401`.
- `API_AUTH_ENABLED` toggle (secure-by-default true once adopted); `/dlq/*` protected alongside `/admin`.
- Migration notes: enable on the trusted network first, register services, then flip Cloudflare ingress to expose `/notify/email` publicly behind both Access and app-layer auth.

## 10. Acceptance criteria

- [ ] `/simplify` applied to all packages; tests green; doc/script drift fixed.
- [ ] Every Go package at ≥90% statement coverage; `make cover` gate passes.
- [ ] Integration tests (mocked downstream + mock SMTP) pass via TDD.
- [ ] `docs/FEATURE_READINESS.md` shows every endpoint exercised locally with real I/O.
- [ ] Deployment artifacts (Dockerfile, compose, cloudflared, systemd, env templates) committed and locally validated; `docs/DEPLOYMENT.md` complete.
- [ ] `docs/INTEGRATION.md` complete with curl/Go/Python examples.
- [ ] `docs/future-scope.md` documents the auth design.
