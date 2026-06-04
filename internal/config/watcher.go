package config

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// ConfigWatcher polls ConfigService on a fixed interval and invokes onChange whenever the revision bumps.
type ConfigWatcher struct {
	service  *ConfigService
	interval time.Duration
	onChange func(*ConfigBundle)
	logger   *slog.Logger
}

func NewConfigWatcher(service *ConfigService, interval time.Duration, onChange func(*ConfigBundle), logger *slog.Logger) *ConfigWatcher {
	return &ConfigWatcher{
		service:  service,
		interval: interval,
		onChange: onChange,
		logger:   logger,
	}
}

// Start runs the polling loop until ctx is cancelled.
func (w *ConfigWatcher) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prevRevision := w.service.GetRevision()
			if err := w.service.RefreshConfig(ctx); err != nil {
				if errors.Is(err, ErrDevModeSkip) {
					continue
				}
				w.logger.Warn("config poll failed", slog.Any("error", err))
				continue
			}
			newRevision := w.service.GetRevision()
			if newRevision > prevRevision {
				bundle := w.service.GetConfig()
				w.logger.Info("config reloaded",
					slog.Int64("revision", newRevision),
					slog.Int("providers", len(bundle.SMTP)),
				)
				w.onChange(bundle)
			}
		}
	}
}
