package main

import (
	"beacon/internal/app"
	confpkg "beacon/internal/config"
	"beacon/internal/models"
	"beacon/internal/notifier"
	"beacon/internal/temporal"
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
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

	taskQueue := notifier.TaskQueueFor(providerName)

	// EmailService is hot-swapped by the ConfigWatcher; guard with an RWMutex.
	var emailSvcMu sync.RWMutex
	emailSvc := notifier.NewEmailService(smtpCfg.Host, smtpCfg.Port, smtpCfg.Username, smtpCfg.Password, smtpCfg.FromAddress, smtpCfg.FromName)

	getEmailService := func() notifier.Notifier[models.EmailMessage] {
		emailSvcMu.RLock()
		defer emailSvcMu.RUnlock()
		return emailSvc
	}

	c, err := client.Dial(envconfig.MustLoadDefaultClientOptions())
	if err != nil {
		log.Fatalln("unable to create Temporal client:", err)
	}
	defer c.Close()

	w := worker.New(c, taskQueue, worker.Options{})

	// Long-lived context — cancelled on SIGTERM to stop the ConfigWatcher.
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		<-sigCh
		watchCancel()
	}()

	pollInterval := app.ParsePollInterval(os.Getenv("CONFIG_POLL_INTERVAL"), 300*time.Second)

	watcher := confpkg.NewConfigWatcher(confpkg.GetConfigService(), pollInterval, func(b *confpkg.ConfigBundle) {
		newCfg, cfgErr := confpkg.GetConfigService().GetClientConfig(providerName)
		if cfgErr != nil {
			logger.Error("config reload: provider not found", slog.String("provider", providerName), slog.Any("error", cfgErr))
			return
		}
		emailSvcMu.Lock()
		emailSvc = notifier.NewEmailService(newCfg.Host, newCfg.Port, newCfg.Username, newCfg.Password, newCfg.FromAddress, newCfg.FromName)
		emailSvcMu.Unlock()
		logger.Info("email service reloaded", slog.String("provider", providerName))
	}, logger)
	go watcher.Start(watchCtx)

	emailActivities := &temporal.EmailActivities{GetService: getEmailService}

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
