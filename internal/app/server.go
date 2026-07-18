// Package app holds the wiring logic for the server and worker binaries,
// extracted from main() so it can be unit-tested.
package app

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"beacon/internal/api"
	"beacon/internal/config"
	"beacon/internal/notifier"
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
