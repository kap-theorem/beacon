package main

import (
	"beacon/internal/api"
	"beacon/internal/app"
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
	pollInterval := app.ParsePollInterval(os.Getenv("CONFIG_POLL_INTERVAL"), 300*time.Second)
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

	addr := ":" + port
	logger.Info("HTTP server starting", slog.String("addr", addr))
	log.Fatal(http.ListenAndServe(addr, mux))
}
