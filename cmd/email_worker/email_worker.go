package main

import (
	"beacon/internal/app"
	"beacon/internal/channel"
	confpkg "beacon/internal/config"
	"beacon/internal/notifier"
	"beacon/internal/temporal"
	"beacon/utils"
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"go.temporal.io/sdk/worker"
)

const configServiceInitTimeout = 60 * time.Second

func main() {
	_ = godotenv.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Init-scoped context for config service startup.
	initCtx, initCancel := context.WithTimeout(context.Background(), configServiceInitTimeout)
	defer initCancel()

	_, err := confpkg.InitializeConfigService(initCtx, logger)
	if err != nil {
		logger.Error("failed to initialize config service at startup", slog.Any("error", err))
		os.Exit(1)
	}

	// Determine which provider this worker instance serves.
	// Set PROVIDER_NAME to the key in the SMTP config map (e.g. "mailgun-payments").
	// If unset, the default provider (is_default: true) is used; if only one provider
	// exists it is automatically the default.
	bundle := confpkg.GetConfigService().GetConfig()
	providerName, smtpCfg, err := app.ResolveWorkerProvider(bundle, os.Getenv("PROVIDER_NAME"))
	if err != nil {
		logger.Error("resolve worker provider", slog.Any("error", err))
		os.Exit(1)
	}

	taskQueue := channel.TaskQueue("email", providerName)

	// EmailSender is hot-swapped by the ConfigWatcher via an atomic pointer.
	var emailSender atomic.Pointer[notifier.EmailSender]
	emailSender.Store(notifier.NewEmailSender(smtpCfg))

	getSender := func() notifier.Sender {
		return emailSender.Load()
	}

	c, err := utils.NewTemporalClient()
	if err != nil {
		logger.Error("unable to create Temporal client", slog.Any("error", err))
		os.Exit(1)
	}
	defer c.Close()

	w := worker.New(c, taskQueue, worker.Options{})

	// Long-lived context — cancelled on SIGTERM/SIGINT to stop the ConfigWatcher.
	watchCtx, watchCancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer watchCancel()

	pollInterval := app.ParsePollInterval(os.Getenv("CONFIG_POLL_INTERVAL"), 300*time.Second)

	watcher := confpkg.NewConfigWatcher(confpkg.GetConfigService(), pollInterval, func(b *confpkg.ConfigBundle) {
		newCfg, cfgErr := confpkg.GetConfigService().GetClientConfig(providerName)
		if cfgErr != nil {
			logger.Error("config reload: provider not found", slog.String("provider", providerName), slog.Any("error", cfgErr))
			return
		}
		old := emailSender.Load()
		emailSender.Store(notifier.NewEmailSender(newCfg))
		if old != nil {
			old.Close()
		}
		logger.Info("email sender reloaded", slog.String("provider", providerName))
	}, logger)
	go watcher.Start(watchCtx)

	emailActivities := &temporal.EmailActivities{GetSender: getSender}

	w.RegisterWorkflow(temporal.SendEmailWorkflow)
	w.RegisterActivity(emailActivities.SendEmailActivity)

	logger.Info("email worker starting",
		slog.String("provider", providerName),
		slog.String("task_queue", taskQueue),
		slog.String("smtp_host", smtpCfg.Host),
	)

	if err = w.Run(worker.InterruptCh()); err != nil {
		logger.Error("worker stopped with error", slog.Any("error", err))
		os.Exit(1)
	}
}
