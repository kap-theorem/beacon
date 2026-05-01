package config

import (
	"context"
	"fmt"
	"log/slog"
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
		svc := &ConfigService{authMethod: "dev", logger: logger}
		svc.Store(bundle)
		globalConfigService = svc
		logger.Info("dev config service initialized", slog.Int("providers", len(bundle.SMTP)))
		return svc, nil
	}

	infisicalAddr := os.Getenv("INFISICAL_ADDR")
	infisicalProjectID := os.Getenv("INFISICAL_PROJECT_ID")
	infisicalEnvironment := os.Getenv("INFISICAL_ENVIRONMENT")

	// Support both old API token and new Machine Identity methods
	infisicalToken := os.Getenv("INFISICAL_TOKEN")
	infisicalAPIKey := os.Getenv("INFISICAL_API_KEY")
	infisicalClientID := os.Getenv("INFISICAL_CLIENT_ID")
	infisicalClientSecret := os.Getenv("INFISICAL_CLIENT_SECRET")

	if infisicalAddr == "" {
		infisicalAddr = "http://localhost:8000"
	}

	// Log which auth method is being used
	if infisicalClientID != "" && infisicalClientSecret != "" {
		logger.Info("using machine identity authentication",
			slog.String("client_id", infisicalClientID[:min(8, len(infisicalClientID))]+"..."),
		)
	} else if infisicalAPIKey != "" {
		logger.Info("using API key authentication")
	} else if infisicalToken != "" {
		logger.Info("using legacy token authentication")
	} else {
		logger.Warn("no infisical credentials provided, using empty auth")
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

	cfg := &SMTPClientConfig{
		Name:       name,
		Provider:   firstNonEmpty(os.Getenv("DEV_SMTP_PROVIDER"), name),
		Host:       host,
		Port:       port,
		Username:   os.Getenv("DEV_SMTP_USERNAME"),
		Password:   os.Getenv("DEV_SMTP_PASSWORD"),
		AuthType:   authType,
		MaxRetries: 3,
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
