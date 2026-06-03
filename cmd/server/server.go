package main

import (
	"beacon/internal/api"
	confpkg "beacon/internal/config"
	"beacon/internal/dlq"
	"beacon/internal/notifier"
	"beacon/utils"
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"go.temporal.io/sdk/client"
)

const configServiceInitTimeout = 30 * time.Second

func main() {
	_ = godotenv.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Init-scoped context for config service startup.
	initCtx, initCancel := context.WithTimeout(context.Background(), configServiceInitTimeout)
	defer initCancel()

	_, err := confpkg.InitializeConfigService(initCtx, logger)
	if err != nil {
		logger.Error("failed to initialize config service", slog.Any("error", err))
		os.Exit(1)
	}

	bundle := confpkg.GetConfigService().GetConfig()
	registry, err := notifier.NewEmailClientRegistry(bundle)
	if err != nil {
		logger.Error("failed to build email client registry", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("email client registry ready", slog.Any("providers", registry.ProviderNames()))

	// Long-lived context — cancelled on SIGTERM/SIGINT to stop background goroutines.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		<-sigCh
		cancel()
	}()

	// ConfigWatcher: poll interval defaults to 300 s, overridden by CONFIG_POLL_INTERVAL (seconds).
	pollInterval := 300 * time.Second
	if raw := os.Getenv("CONFIG_POLL_INTERVAL"); raw != "" {
		if secs, parseErr := strconv.Atoi(raw); parseErr == nil && secs > 0 {
			pollInterval = time.Duration(secs) * time.Second
		}
	}
	watcher := confpkg.NewConfigWatcher(confpkg.GetConfigService(), pollInterval, func(b *confpkg.ConfigBundle) {
		if reloadErr := registry.Reload(b); reloadErr != nil {
			logger.Error("registry reload failed", slog.Any("error", reloadErr))
		}
	}, logger)
	go watcher.Start(ctx)

	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "6969"
	}

	var temporalClient client.Client
	tc, err := utils.NewTemporalClient()
	if err != nil {
		logger.Warn("temporal client unavailable, email and DLQ endpoints will not work", slog.Any("error", err))
	} else {
		defer tc.Close()
		temporalClient = tc
	}

	email := &api.EmailHandler{
		TemporalClient: temporalClient,
		Registry:       registry,
	}

	adminHandler := api.NewAdminHandler(confpkg.GetConfigService(), registry, logger)

	healthChecker := confpkg.NewHealthChecker()
	healthChecker.SetReady(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/notify/email", email.HandleRequest)
	mux.HandleFunc("/healthz/live", healthChecker.HandleLive)
	mux.HandleFunc("/healthz/ready", healthChecker.HandleReady)
	mux.HandleFunc("/admin/config/refresh", adminHandler.HandleConfigRefresh)

	if temporalClient != nil {
		namespace := os.Getenv("TEMPORAL_NAMESPACE")
		if namespace == "" {
			namespace = "default"
		}
		dlqService := dlq.NewDLQService(temporalClient, namespace, logger)
		dlqHandler := api.NewDLQHandler(dlqService, logger)
		mux.HandleFunc("/dlq/failed", dlqHandler.HandleQueryFailures)
		mux.HandleFunc("/dlq/replay/", dlqHandler.HandleReplay)
	} else {
		unavailable := func(w http.ResponseWriter, r *http.Request) {
			utils.WriteError(w, http.StatusServiceUnavailable, "temporal service not available")
		}
		mux.HandleFunc("/dlq/failed", unavailable)
		mux.HandleFunc("/dlq/replay/", unavailable)
	}

	addr := ":" + port
	logger.Info("HTTP server starting", slog.String("addr", addr))
	log.Fatal(http.ListenAndServe(addr, mux))
}
