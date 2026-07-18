package config

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

// TestRefreshConfig_DevModeSkipsInfisical guards against the nil-httpClient
// panic: in DEV_MODE the ConfigService is built without an Infisical endpoint,
// so RefreshConfig must return ErrDevModeSkip rather than reaching fetchConfigs.
func TestRefreshConfig_DevModeSkipsInfisical(t *testing.T) {
	t.Setenv("DEV_MODE", "true")
	t.Setenv("DEV_SMTP_HOST", "localhost")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := InitializeConfigService(context.Background(), logger)
	if err != nil {
		t.Fatalf("InitializeConfigService: %v", err)
	}

	err = svc.RefreshConfig(context.Background())
	if !errors.Is(err, ErrDevModeSkip) {
		t.Errorf("RefreshConfig in DEV_MODE: got %v, want ErrDevModeSkip", err)
	}
}
