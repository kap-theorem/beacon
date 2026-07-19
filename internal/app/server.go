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
	notify := &api.NotifyHandler{
		TemporalClient: d.TemporalClient, Channels: d.Channels,
		Providers: d.Providers, Limiter: d.Limiter, Logger: d.Logger,
	}
	adminHandler := api.NewAdminHandler(d.ConfigService, d.Providers, d.AuthRegistry, d.Logger)
	authMW := auth.Middleware(d.AuthRegistry)

	mux := http.NewServeMux()
	mux.Handle("POST /v1/notify/{channel}", authMW(http.HandlerFunc(notify.Handle)))
	mux.HandleFunc("/healthz/live", d.Health.HandleLive)
	mux.HandleFunc("/healthz/ready", d.Health.HandleReady)
	mux.HandleFunc("/admin/config/refresh", adminHandler.HandleConfigRefresh)

	if d.DLQService != nil {
		dh := api.NewDLQHandler(d.DLQService, d.Logger)
		mux.Handle("GET /v1/dlq/failed", authMW(http.HandlerFunc(dh.HandleQueryFailures)))
		mux.Handle("POST /v1/dlq/replay/{workflowID}", authMW(http.HandlerFunc(dh.HandleReplay)))
	} else {
		unavailable := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			utils.WriteError(w, http.StatusServiceUnavailable, "temporal service not available")
		})
		mux.Handle("GET /v1/dlq/failed", authMW(unavailable))
		mux.Handle("POST /v1/dlq/replay/{workflowID}", authMW(unavailable))
	}
	return mux
}
