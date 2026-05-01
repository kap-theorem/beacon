package main

import (
	confpkg "beacon/internal/config"
	"beacon/internal/api"
	"beacon/utils"
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
	"go.temporal.io/sdk/client"
)

const configServiceInitTimeout = 30 * time.Second

func main() {
	_ = godotenv.Load()

	ctx, cancel := context.WithTimeout(context.Background(), configServiceInitTimeout)
	defer cancel()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Initialize config service (Infisical or dev mode)
	_, err := confpkg.InitializeConfigService(ctx, logger)
	if err != nil {
		logger.Error("failed to initialize config service", slog.Any("error", err))
		os.Exit(1)
	}

	taskQueue := os.Getenv("EMAIL_NOTIFIER_TASK_QUEUE")
	if taskQueue == "" {
		taskQueue = "email-task-queue"
	}

	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "6969"
	}
	httpAddr := ":" + port

	var temporalClient client.Client

	tc, err := utils.NewTemporalClient()
	if err != nil {
		logger.Warn("temporal client unavailable, email endpoint will not work", slog.Any("error", err))
		temporalClient = nil
	} else {
		defer tc.Close()
		temporalClient = tc
	}

	email := &api.EmailHandler{
		TemporalClient:         temporalClient,
		EmailNotifierTaskQueue: taskQueue,
	}

	healthChecker := confpkg.NewHealthChecker()
	healthChecker.SetReady(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/notify/email", email.HandleRequest)
	mux.HandleFunc("/healthz/live", healthChecker.HandleLive)
	mux.HandleFunc("/healthz/ready", healthChecker.HandleReady)

	logger.Info("HTTP server starting", slog.String("addr", httpAddr))
	log.Fatal(http.ListenAndServe(httpAddr, mux))
}
