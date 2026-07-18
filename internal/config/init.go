package config

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"
)

var globalConfigService *ConfigService

func InitializeConfigService(ctx context.Context, logger *slog.Logger) (*ConfigService, error) {
	if os.Getenv("DEV_MODE") == "true" {
		logger.Info("dev mode enabled: loading config from environment variables")
		bundle, err := buildDevBundle()
		if err != nil {
			return nil, fmt.Errorf("failed to build dev config bundle: %w", err)
		}
		svc := &ConfigService{
			authMethod: "dev",
			logger:     logger,
			httpClient: &http.Client{Timeout: 10 * time.Second},
		}
		svc.Store(bundle)
		globalConfigService = svc
		logger.Info("dev config service initialized", slog.Int("providers", len(bundle.SMTP)))
		return svc, nil
	}

	infisicalAddr := os.Getenv("INFISICAL_ADDR")
	infisicalProjectID := os.Getenv("INFISICAL_PROJECT_ID")
	infisicalEnvironment := os.Getenv("INFISICAL_ENVIRONMENT")

	// Support both API key and Machine Identity methods
	infisicalAPIKey := os.Getenv("INFISICAL_API_KEY")
	infisicalClientID := os.Getenv("INFISICAL_CLIENT_ID")
	infisicalClientSecret := os.Getenv("INFISICAL_CLIENT_SECRET")

	if infisicalAddr == "" {
		infisicalAddr = "http://localhost:8000"
	}

	service := NewConfigService(infisicalAddr, infisicalProjectID, infisicalEnvironment, infisicalAPIKey, infisicalClientID, infisicalClientSecret, logger)

	bundle, err := service.LoadWithRetry(ctx)
	if err != nil {
		logger.Error("failed to load config at startup",
			slog.Any("error", err),
		)
		return nil, err
	}

	service.Store(bundle)
	globalConfigService = service

	logger.Info("config service initialized",
		slog.Int("providers", len(bundle.SMTP)),
		slog.Int64("revision", bundle.Revision),
		slog.String("auth_method", service.authMethod),
	)

	return service, nil
}

func GetConfigService() *ConfigService {
	return globalConfigService
}

func buildDevBundle() (*ConfigBundle, error) {
	host := os.Getenv("DEV_SMTP_HOST")
	if host == "" {
		return nil, fmt.Errorf("DEV_SMTP_HOST must be set when DEV_MODE=true")
	}

	port := 587
	if s := os.Getenv("DEV_SMTP_PORT"); s != "" {
		p, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("invalid DEV_SMTP_PORT: %w", err)
		}
		port = p
	}

	name := os.Getenv("DEV_SMTP_NAME")
	if name == "" {
		name = "dev"
	}

	authType := AuthType(os.Getenv("DEV_SMTP_AUTH_TYPE"))
	if authType == "" {
		authType = AuthPlain
	}

	fromAddr := os.Getenv("DEV_SMTP_FROM")
	if fromAddr == "" {
		fromAddr = firstNonEmpty(os.Getenv("DEV_SMTP_USERNAME"), "noreply@beacon.local")
	}

	cfg := &SMTPClientConfig{
		Name:        name,
		Provider:    firstNonEmpty(os.Getenv("DEV_SMTP_PROVIDER"), name),
		Host:        host,
		Port:        port,
		Username:    os.Getenv("DEV_SMTP_USERNAME"),
		Password:    os.Getenv("DEV_SMTP_PASSWORD"),
		AuthType:    authType,
		FromAddress: fromAddr,
		FromName:    firstNonEmpty(os.Getenv("DEV_SMTP_FROM_NAME"), "Beacon"),
	}

	return &ConfigBundle{
		SMTP:      map[string]*SMTPClientConfig{name: cfg},
		Revision:  1,
		Timestamp: time.Now().UTC(),
	}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
