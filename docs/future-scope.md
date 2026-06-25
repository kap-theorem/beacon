# Beacon — Future Scope: Per-Service API-Key Authentication

**Status:** Design document only. Nothing in this document is implemented.
**Target audience:** Future implementors and reviewers.

---

## 1. Motivation and Current Gap

Beacon currently exposes two categories of operational endpoints with no application-layer authentication:

- `POST /notify/email` — submits an email notification workflow
- `GET /dlq/failed` and `POST /dlq/replay/{workflowID}` — inspect and replay failed workflows

The only protection today is perimeter-level: a Cloudflare Tunnel with a Cloudflare Access policy restricts which clients can reach the server at the network layer (see `deploy/docker-compose.yml` and the `deploy/cloudflared/` configuration). The deployment runbook and the integration guide describe this assumption explicitly.

This perimeter approach is appropriate as a baseline but insufficient for a production environment shared by multiple upstream services. The specific gaps are:

1. **`/notify/email` is an open relay at the application layer.** Any request that arrives through the Cloudflare tunnel is accepted without identifying which upstream service sent it. If Cloudflare Access is misconfigured, momentarily bypassed, or the tunnel is exposed on an internal network, any client can send arbitrary email through Beacon.

2. **No per-service scoping.** Beacon supports routing categories (e.g., `"transactional"`, `"otp"`, `"marketing"`) mapped to specific SMTP providers via the `categories` field in `SMTPClientConfig` and resolved by `EmailClientRegistry.Resolve`. There is no mechanism today to restrict a specific upstream service to only the categories it is authorized to use. A service responsible for OTP email could also send marketing bulk mail by changing `client_hint`.

3. **The `/dlq/*` endpoints are high-impact.** Replaying a workflow re-delivers an email. Without auth, any client that gets past the perimeter can trigger replays.

4. **No audit trail per caller.** Logs produced by `internal/api/email.go` and `internal/api/dlq.go` do not record a caller identity. Debugging mis-deliveries or abuse requires correlating network logs outside Beacon.

Per-service API-key authentication resolves all four gaps with minimal complexity and no external dependencies beyond what Beacon already uses (Infisical for secret storage, the existing `ConfigService`/`ConfigWatcher` pattern for hot-reloaded config).

---

## 2. Proposed Design: `internal/auth` Package

### Package responsibilities

A new package `internal/auth` provides:

- `APIKeyRecord` — the per-service record shape (see Section 3)
- `AuthService` — loads and holds the live key-records map; validates individual requests
- `Middleware` — an `http.Handler` wrapper that enforces auth and populates request context with caller identity

### Mirroring the existing ConfigService pattern

`ConfigService` in `internal/config/service.go` follows a clear lifecycle:

1. At startup, `InitializeConfigService` detects `DEV_MODE` and either builds a bundle from env vars or loads from Infisical via `LoadWithRetry`.
2. The resulting bundle is stored via `Store` and readable via `GetConfig`/`GetClientConfig` under a `sync.RWMutex`.
3. `ConfigWatcher` polls `RefreshConfig` on a fixed interval. When the revision bumps, it fires the `onChange` callback, which in turn calls `Reload` on `EmailClientRegistry`.

`AuthService` follows an identical lifecycle:

1. At startup, `InitializeAuthService` detects `DEV_MODE` and either parses `DEV_API_KEYS` from the environment or fetches the `/beacon/api-keys` secret path from Infisical via the shared `ConfigService` HTTP client / access-token logic.
2. The resulting map of `map[string]APIKeyRecord` (keyed by `key_hash`) is held under a `sync.RWMutex`.
3. The existing `ConfigWatcher` calls a second `onChange` callback that invokes `AuthService.Reload` whenever the config revision bumps.

This reuse means no new polling infrastructure, no new Infisical authentication path, and hot-revocation of keys happens on the same interval as SMTP config refresh.

### Sketch of the type

```go
// Package auth provides per-service API-key authentication for Beacon's HTTP layer.
package auth

import (
    "crypto/sha256"
    "crypto/subtle"
    "encoding/hex"
    "fmt"
    "net/http"
    "sync"
)

// APIKeyRecord describes a single registered API key.
type APIKeyRecord struct {
    Name               string   `json:"name"`                // human-readable service name, e.g. "auth-service"
    KeyHash            string   `json:"key_hash"`            // lowercase hex sha256 of the raw key
    AllowedCategories  []string `json:"allowed_categories"`  // routing categories this key may use; empty means all
    Enabled            bool     `json:"enabled"`
}

// AuthService holds the live set of API-key records and validates requests.
type AuthService struct {
    mu      sync.RWMutex
    records map[string]APIKeyRecord // keyed by KeyHash for O(1) lookup
    enabled bool                    // mirrors API_AUTH_ENABLED toggle
}

// Validate checks the presented bearer token against the stored records.
// It returns the matching APIKeyRecord and nil on success.
// It returns an error whose type (ErrUnauthorized or ErrForbidden) the caller uses
// to write the appropriate HTTP status.
func (s *AuthService) Validate(presentedKey string, resolvedCategory string) (APIKeyRecord, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()

    if !s.enabled {
        return APIKeyRecord{}, nil // auth globally disabled; pass through
    }

    if presentedKey == "" {
        return APIKeyRecord{}, ErrUnauthorized
    }

    hash := sha256Hex(presentedKey)

    rec, ok := s.records[hash]
    if !ok {
        return APIKeyRecord{}, ErrUnauthorized
    }

    // Constant-time compare to prevent timing-based enumeration.
    // The hash comparison above is already a fixed-length string equality,
    // but using ConstantTimeCompare on the byte representations eliminates
    // any Go string-equality short-circuit.
    hashBytes := []byte(hash)
    storedBytes := []byte(rec.KeyHash)
    if subtle.ConstantTimeCompare(hashBytes, storedBytes) != 1 {
        return APIKeyRecord{}, ErrUnauthorized
    }

    if !rec.Enabled {
        return APIKeyRecord{}, ErrUnauthorized
    }

    if !categoryAllowed(rec.AllowedCategories, resolvedCategory) {
        return APIKeyRecord{}, ErrForbidden
    }

    return rec, nil
}

// Reload atomically replaces the live record set.
func (s *AuthService) Reload(records []APIKeyRecord, enabled bool) {
    m := make(map[string]APIKeyRecord, len(records))
    for _, r := range records {
        m[r.KeyHash] = r
    }
    s.mu.Lock()
    s.records = m
    s.enabled = enabled
    s.mu.Unlock()
}

func sha256Hex(key string) string {
    sum := sha256.Sum256([]byte(key))
    return hex.EncodeToString(sum[:])
}

func categoryAllowed(allowed []string, requested string) bool {
    if len(allowed) == 0 {
        return true // empty list means unrestricted
    }
    for _, a := range allowed {
        if a == requested {
            return true
        }
    }
    return false
}
```

---

## 3. Key Storage

### Production path: Infisical `/beacon/api-keys`

API-key records are stored as individual secrets under the path `/beacon/api-keys` in Infisical — the same project and environment already used for SMTP config at `/beacon/smtp`.

Each secret's key is the service name (e.g., `auth-service`) and the value is a JSON-encoded `APIKeyRecord`:

```json
{
  "name": "auth-service",
  "key_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "allowed_categories": ["otp", "transactional"],
  "enabled": true
}
```

**The raw key is never stored.** Only the lowercase hex SHA-256 hash of the raw key is stored in Infisical. The raw key is generated once by the operator (see Section 7), shown once, and then discarded. If a key needs to be rotated, a new key is generated, the record's `key_hash` is updated in Infisical, and the hot-reload cycle propagates the change to all running instances within one poll interval.

`fetchConfigs` in `internal/config/service.go` already fetches all secrets under a given path as a `map[string]string`. `AuthService` initialization calls the same method with `basePath = "/beacon/api-keys"` and unmarshals each value into `APIKeyRecord`.

### Dev path: `DEV_API_KEYS` environment variable

When `DEV_MODE=true`, the production Infisical path is skipped. Instead, the operator sets:

```
DEV_API_KEYS=[{"name":"local-test","key_hash":"<sha256-of-your-test-key>","allowed_categories":[],"enabled":true}]
```

This is a JSON array of `APIKeyRecord` objects. `InitializeAuthService` detects `DEV_MODE=true`, parses this env var, and calls `AuthService.Reload` with the result. If `DEV_API_KEYS` is not set, the auth service starts with an empty record set (all requests allowed when auth is disabled, or all requests rejected when enabled, depending on `API_AUTH_ENABLED`).

This mirrors the existing pattern in `internal/config/init.go` where `DEV_MODE=true` triggers `buildDevBundle()` from `DEV_SMTP_*` env vars instead of calling Infisical.

---

## 4. Request Flow

### Wire-level

Callers include their API key as a standard HTTP Bearer token:

```
POST /notify/email HTTP/1.1
Authorization: Bearer bkn_svc_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
Content-Type: application/json
```

### Server-side flow

```
Client
  |
  | Authorization: Bearer <raw-key>
  v
+-----------------------------------+
| HTTP Server                       |
|                                   |
|  AuthMiddleware                   |
|  1. Extract token from header     |
|  2. sha256(raw-key) -> hash       |
|  3. Look up hash in AuthService   |
|  4. ConstantTimeCompare(hash,     |
|        record.KeyHash)            |
|  5. Check record.Enabled          |
|  6. Resolve category from         |
|        request body client_hint   |
|     via Registry.Resolve          |
|  7. Check category in             |
|        record.AllowedCategories   |
+-----------------------------------+
       |                 |
    401/403         proceed to
    (generic        EmailHandler
     message)       or DLQHandler
```

### Category resolution detail

The routing category is resolved from the `client_hint` field in the request body using `EmailClientRegistry.Resolve`. This is the same resolution performed by `EmailHandler.HandleRequest` in `internal/api/email.go`. The middleware resolves the category first (before the handler does) so it can enforce scope.

For the DLQ endpoints (`/dlq/failed`, `/dlq/replay/`), there is no `client_hint`. The middleware enforces a reserved category string `"dlq"` for these routes. Any key that needs DLQ access must include `"dlq"` in its `allowed_categories`. Operators who want a key with unrestricted access (including DLQ) set `allowed_categories` to `[]` (empty = unrestricted).

### Pseudocode: middleware

```go
func (m *AuthMiddleware) Wrap(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        token := extractBearer(r.Header.Get("Authorization"))
        category := resolvedCategory(r, m.registry) // reads body peek for /notify/email; "dlq" for /dlq/*
        rec, err := m.authSvc.Validate(token, category)
        if err != nil {
            if errors.Is(err, ErrForbidden) {
                utils.WriteError(w, http.StatusForbidden, "insufficient permissions")
                return
            }
            utils.WriteError(w, http.StatusUnauthorized, "authentication required")
            return
        }
        ctx := context.WithValue(r.Context(), callerKey{}, rec.Name)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

The caller's `rec.Name` is stored in the request context. Handlers and log statements downstream can retrieve it for structured logging.

---

## 5. Failure Modes and Status Codes

| Condition | HTTP Status | Response body | Rationale |
|---|---|---|---|
| `Authorization` header absent or not a Bearer token | 401 | `{"success":false,"error":"authentication required"}` | Standard RFC 6750 behavior for missing credentials |
| Token presented but no matching hash in records | 401 | `{"success":false,"error":"authentication required"}` | Indistinguishable from absent — prevents key enumeration |
| Token matches a record where `enabled: false` | 401 | `{"success":false,"error":"authentication required"}` | Same 401 as invalid key; do not reveal that the key is recognized but disabled |
| Token valid and enabled, but `resolved_category` not in `allowed_categories` | 403 | `{"success":false,"error":"insufficient permissions"}` | The caller is authenticated but not authorized for this category; 403 is appropriate |
| Auth globally disabled (`API_AUTH_ENABLED=false`) | — | pass through | No auth overhead on internal-only deployments |

**On the disabled-key response:** returning 401 rather than 403 for a disabled key is a deliberate choice to avoid leaking that the key exists in the system. An operator who wants to signal key expiry in a non-security context can add a comment to the Infisical record; from the wire perspective the response is identical to an unknown key.

**Response bodies are generic.** Error messages never include the presented token, the hash, or the record name. This prevents both key enumeration and inadvertent credential leakage in logs.

---

## 6. Toggle and Rollout

### Environment toggle

```
API_AUTH_ENABLED=false   # default — backward-compatible, no auth enforced
API_AUTH_ENABLED=true    # enforce authentication on protected routes
```

When `API_AUTH_ENABLED=false`, `AuthService.Validate` returns immediately with no error (see the `!s.enabled` early return in Section 2). All existing behavior is preserved. Existing callers that do not send an `Authorization` header continue to work.

### Protected routes

When auth is enabled, the middleware wraps:

- `POST /notify/email`
- `GET /dlq/failed`
- `POST /dlq/replay/{workflowID}`

Health check endpoints (`/healthz/live`, `/healthz/ready`) and the admin config-refresh endpoint (`/admin/config/refresh`) are not covered by API-key auth. The admin endpoint already has its own `ADMIN_TOKEN` bearer check.

### Migration plan

The recommended rollout sequence is:

**Step (a) — Report-only mode (internal first).** Deploy with `API_AUTH_ENABLED=false` and the `AuthService` initialized. Add structured log entries in the middleware that record whether a request would have passed or failed auth, without rejecting it. This lets operators observe which callers are and are not sending keys without causing outages.

**Step (b) — Register downstream services and issue keys.** For each upstream service that calls `/notify/email`, generate a key with `make genkey` (see Section 7), store the hash in Infisical under `/beacon/api-keys`, and provide the raw key to the service team. Configure `allowed_categories` to the minimum set that service needs. The hot-reload cycle propagates the new record without a Beacon restart.

**Step (c) — Flip to enforce.** Set `API_AUTH_ENABLED=true`. Verify that all registered services authenticate successfully by checking logs. Any service returning 401s has not yet sent its key and needs follow-up.

**Step (d) — Defense in depth.** Once all services are authenticated at the application layer, the deployment can be fully exposed behind Cloudflare Access (network layer) plus API-key auth (application layer). The two controls are independent: a compromised Cloudflare policy does not bypass Beacon's auth, and an accidentally leaked key is bounded by the Cloudflare network perimeter.

---

## 7. Key Management Helper: `make genkey`

Add a `genkey` Makefile target backed by a small Go program (or a shell one-liner using `openssl` + `sha256sum`) that:

1. Generates 32 bytes of cryptographically random data and base64url-encodes them into a `bkn_svc_<random>` string (the `bkn_svc_` prefix makes keys greppable and helps secret-scanning tools identify leaks).
2. Computes the lowercase hex SHA-256 of that string.
3. Prints the raw key **once** to stdout with a clear "SAVE THIS — it will not be shown again" warning.
4. Emits a JSON record template ready to paste into Infisical:

```makefile
.PHONY: genkey
genkey:
	go run ./scripts/genkey/main.go
```

Example output:

```
=== Beacon API Key Generator ===

RAW KEY (save this now, it is shown only once):
  bkn_svc_8f3kLmNpQrStUvWxYzAbCdEfGhIjKlMnOpQrStUv

SHA-256 HASH (store this in Infisical):
  e9a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1

Infisical record template (/beacon/api-keys, key = <service-name>):
{
  "name": "<service-name>",
  "key_hash": "e9a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1",
  "allowed_categories": ["transactional"],
  "enabled": true
}
```

The script in `scripts/genkey/main.go` uses `crypto/rand` for key generation and `crypto/sha256` for hashing — the same primitives used in `AuthService.Validate`. It never writes the raw key to disk.

---

## 8. Testing Strategy

The following test plan is intended for the implementation phase. It is not exhaustive but covers the critical correctness properties.

### Unit tests (`internal/auth`)

**Hash and constant-time compare**

Table-driven tests over `sha256Hex` and `AuthService.Validate` hash-matching path:

| Case | Input | Expected outcome |
|---|---|---|
| Correct raw key | Matching raw key for a stored hash | `nil` error, record returned |
| Wrong key, correct length | Different raw key of same length | `ErrUnauthorized` |
| Empty key | `""` | `ErrUnauthorized` |
| Key off by one character | Almost-correct key | `ErrUnauthorized` |
| Timing: correct vs. wrong | Measure that latency difference is negligible | Use `testing.B` with a constant-time assertion comment |

**Category-scope enforcement**

| Case | `allowed_categories` | `resolvedCategory` | Expected outcome |
|---|---|---|---|
| Exact match | `["otp"]` | `"otp"` | `nil` |
| Unrestricted | `[]` | any | `nil` |
| Category not in list | `["otp"]` | `"marketing"` | `ErrForbidden` |
| DLQ access not granted | `["otp"]` | `"dlq"` | `ErrForbidden` |
| DLQ access granted | `["dlq"]` | `"dlq"` | `nil` |

**Enabled flag**

| Case | `enabled` | Expected outcome |
|---|---|---|
| Enabled key | `true` | `nil` |
| Disabled key | `false` | `ErrUnauthorized` (not `ErrForbidden`) |

**Global toggle**

| `API_AUTH_ENABLED` | Any presented key | Expected outcome |
|---|---|---|
| `false` | absent | `nil` (pass-through) |
| `false` | wrong key | `nil` (pass-through) |
| `true` | absent | `ErrUnauthorized` |
| `true` | correct key | `nil` |

### Hot-reload test

Construct an `AuthService` with a valid key record. Call `Validate` with the correct key — asserts pass. Call `Reload` with an empty record set (simulating revocation). Call `Validate` again with the same key — asserts `ErrUnauthorized`. This tests the `sync.RWMutex` swap path without any I/O.

### Integration test: revoked key returns 401 after config refresh

This test requires a running Beacon server wired to a test `AuthService`:

1. Start the server with `API_AUTH_ENABLED=true` and one valid key record.
2. Assert that `POST /notify/email` with the correct bearer token returns `202`.
3. Call `AuthService.Reload` with an empty record set (or a record with `enabled: false`).
4. Assert that `POST /notify/email` with the same token now returns `401`.
5. Assert that the response body is the generic `{"success":false,"error":"authentication required"}` (no key details leaked).

This is the most important integration test because it validates the hot-revocation path end-to-end.

### Middleware tests

Drive `AuthMiddleware.Wrap` via `httptest.NewRecorder` with a stub inner handler that always returns `200`. Table-driven over:
- Missing header → 401, inner handler not called
- Malformed header (not `Bearer ...`) → 401
- Wrong token → 401
- Correct token, wrong category → 403
- Correct token, correct category → 200, inner handler called, request context contains caller name

---

## 9. Open Questions and Alternatives

### Alternative: mTLS via Cloudflare

Cloudflare Access supports mTLS client certificates at the tunnel/CDN layer. This would authenticate callers before any request reaches Beacon, with no changes to Beacon's code. The tradeoff is that mTLS is managed entirely in Cloudflare configuration, making it invisible to Beacon's own logs and requiring operators to manage certificate issuance for each upstream service. It also does not support the per-category routing scope constraint that is native to Beacon's `EmailClientRegistry.categories` model.

mTLS is a strong complement to API-key auth but does not replace it for Beacon's use case, where the categories concept is central to access control.

### Alternative: JWT / OIDC

JWT bearer tokens issued by a central identity provider (e.g., a Keycloak or Auth0 instance) would allow claims-based authorization and token revocation via short lifetimes. The operationally simpler self-hosted environment Beacon targets (home server, self-hosted Temporal) does not currently include an OIDC provider, and adding one solely for Beacon's auth needs is disproportionate. JWT can be introduced later if Beacon is deployed in an environment that already has OIDC infrastructure.

### Why per-service API keys are the right near-term step

API keys stored in Infisical are consistent with how Beacon already manages all its secrets. They require no new infrastructure. They can be issued, scoped, and revoked by a single operator through the Infisical UI or API. The hot-reload mechanism that Beacon already has for SMTP config makes key revocation take effect within one poll interval without a deployment. The `make genkey` helper keeps the raw key off disk and out of version control.

The design is intentionally minimal: it solves the open-relay and per-service scoping problems with the least new moving parts, and leaves room to layer mTLS or OIDC on top later without reworking the category-scope model.
