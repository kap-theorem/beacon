// Package app holds the wiring logic for the server and worker binaries,
// extracted from main() so it can be unit-tested.
package app

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"beacon/internal/api"
	"beacon/internal/auth"
	"beacon/internal/channel"
	"beacon/internal/config"
	"beacon/internal/notifier"
	"beacon/internal/policy"
	"beacon/utils"
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
	LegacyRegistry *notifier.EmailClientRegistry // removed at cutover (Task 12)
	Channels       channel.Registry
	Providers      *notifier.ProviderRegistry
	AuthRegistry   *auth.Registry
	Limiter        policy.RateLimiter
	ConfigService  *config.ConfigService
	Health         *config.HealthChecker
	DLQService     api.DLQQuerier // nil when Temporal is unavailable
	Logger         *slog.Logger
}

// BuildServerMux wires all HTTP routes. /v1 routes run behind auth middleware.
func BuildServerMux(d ServerDeps) *http.ServeMux {
	legacy := &api.EmailHandler{TemporalClient: d.TemporalClient, Registry: d.LegacyRegistry}
	notify := &api.NotifyHandler{
		TemporalClient: d.TemporalClient, Channels: d.Channels,
		Providers: d.Providers, Limiter: d.Limiter, Logger: d.Logger,
	}
	adminHandler := api.NewAdminHandler(d.ConfigService, d.Providers, d.AuthRegistry, d.LegacyRegistry, d.Logger)
	authMW := auth.Middleware(d.AuthRegistry)

	mux := http.NewServeMux()
	mux.HandleFunc("/notify/email", legacy.HandleRequest) // deleted at cutover
	mux.Handle("POST /v1/notify/{channel}", authMW(http.HandlerFunc(notify.Handle)))
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
