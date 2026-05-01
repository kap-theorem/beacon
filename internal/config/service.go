package config

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type ConfigService struct {
	infisicalAddr string
	projectID     string
	environment   string
	apiKey        string
	clientID      string
	clientSecret  string
	authMethod    string // "client-secret", "api-key", or "token"
	httpClient    *http.Client

	mu              sync.RWMutex
	current         *ConfigBundle
	previous        *ConfigBundle
	revision        int64
	accessToken     string
	accessTokenExp  time.Time

	logger *slog.Logger
}

func NewConfigService(infisicalAddr, projectID, environment, apiKey, clientID, clientSecret string, logger *slog.Logger) *ConfigService {
	if logger == nil {
		logger = slog.Default()
	}

	if environment == "" {
		environment = "prod"
	}

	cs := &ConfigService{
		infisicalAddr: infisicalAddr,
		projectID:     projectID,
		environment:   environment,
		apiKey:        apiKey,
		clientID:      clientID,
		clientSecret:  clientSecret,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger:   logger,
		revision: 0,
	}

	// Determine auth method
	if clientID != "" && clientSecret != "" {
		cs.authMethod = "client-secret"
	} else if apiKey != "" {
		cs.authMethod = "api-key"
	} else {
		cs.authMethod = "token" // fallback for backward compatibility
	}

	return cs
}

func (cs *ConfigService) getAccessToken(ctx context.Context) (string, error) {
	cs.mu.RLock()
	if cs.accessToken != "" && time.Now().Before(cs.accessTokenExp.Add(-30*time.Second)) {
		defer cs.mu.RUnlock()
		return cs.accessToken, nil
	}
	cs.mu.RUnlock()

	if cs.authMethod != "client-secret" {
		return cs.apiKey, nil
	}

	formData := url.Values{}
	formData.Set("clientId", cs.clientID)
	formData.Set("clientSecret", cs.clientSecret)

	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/api/v1/auth/universal-auth/login", cs.infisicalAddr),
		strings.NewReader(formData.Encode()))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := cs.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
			return "", &TransientError{msg: fmt.Sprintf("auth error: HTTP %d", resp.StatusCode)}
		}
		return "", fmt.Errorf("authentication failed: HTTP %d", resp.StatusCode)
	}

	var result struct {
		AccessToken string `json:"accessToken"`
		ExpiresIn   int64  `json:"expiresIn"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode auth response: %w", err)
	}

	cs.mu.Lock()
	cs.accessToken = result.AccessToken
	cs.accessTokenExp = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	cs.mu.Unlock()

	return result.AccessToken, nil
}

var backoffSchedule = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
}

func (cs *ConfigService) LoadWithRetry(ctx context.Context) (*ConfigBundle, error) {
	var lastErr error

	for attempt, backoff := range backoffSchedule {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		bundle, err := cs.loadFromInfisical(ctx)
		if err == nil {
			cs.logger.Info("config loaded successfully",
				slog.Int("providers", len(bundle.SMTP)),
				slog.Int64("revision", bundle.Revision),
				slog.Int("attempt", attempt+1),
			)
			return bundle, nil
		}

		lastErr = err
		if !isTransientError(err) {
			cs.logger.Error("non-transient infisical error, failing fast",
				slog.Any("error", err),
				slog.Int("attempt", attempt+1),
			)
			return nil, err
		}

		if attempt < len(backoffSchedule)-1 {
			cs.logger.Warn("infisical unreachable, retrying",
				slog.Any("error", err),
				slog.Int("attempt", attempt+1),
				slog.Duration("backoff", backoff),
			)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	return nil, fmt.Errorf("config load failed after 5 attempts: %w", lastErr)
}

func (cs *ConfigService) loadFromInfisical(ctx context.Context) (*ConfigBundle, error) {
	bundle := &ConfigBundle{
		SMTP:      make(map[string]*SMTPClientConfig),
		Timestamp: time.Now().UTC(),
	}

	smtpConfigs, err := cs.fetchConfigs(ctx, "/beacon/smtp")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch SMTP configs: %w", err)
	}

	for path, rawJSON := range smtpConfigs {
		cfg, valErr := ValidateConfig(rawJSON)
		if valErr != nil {
			return nil, fmt.Errorf("validation error at %s: %w", path, valErr)
		}
		bundle.SMTP[cfg.Name] = cfg
	}

	cs.mu.Lock()
	cs.revision++
	bundle.Revision = cs.revision
	cs.mu.Unlock()

	return bundle, nil
}

func (cs *ConfigService) fetchConfigs(ctx context.Context, basePath string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		fmt.Sprintf("%s/api/v4/secrets?projectId=%s&environment=%s&secretPath=%s",
			cs.infisicalAddr, cs.projectID, cs.environment, basePath), nil)
	if err != nil {
		return nil, err
	}

	// Get authorization token
	var token string
	switch cs.authMethod {
	case "client-secret":
		var err error
		token, err = cs.getAccessToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get access token: %w", err)
		}
	case "api-key":
		token = cs.apiKey
	default:
		token = cs.apiKey
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("Accept", "application/json")

	resp, err := cs.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
			return nil, &TransientError{msg: fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}
		return nil, fmt.Errorf("infisical error: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Secrets []struct {
			Key       string `json:"secretKey"`
			Value     string `json:"secretValue"`
			Comment   string `json:"secretComment"`
		} `json:"secrets"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode infisical response: %w", err)
	}

	configs := make(map[string]string)
	for _, secret := range result.Secrets {
		configs[secret.Key] = secret.Value
	}

	return configs, nil
}

func (cs *ConfigService) Store(bundle *ConfigBundle) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.current != nil {
		cs.previous = cs.current
	}
	cs.current = bundle
}

func (cs *ConfigService) GetClientConfig(providerName string) (*SMTPClientConfig, error) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if cs.current == nil {
		return nil, ErrConfigNotInitialized
	}

	cfg, ok := cs.current.SMTP[providerName]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotFound, providerName)
	}

	return cfg, nil
}

func (cs *ConfigService) GetConfig() *ConfigBundle {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if cs.current == nil {
		return nil
	}

	bundle := &ConfigBundle{
		SMTP:      make(map[string]*SMTPClientConfig),
		Revision:  cs.current.Revision,
		Timestamp: cs.current.Timestamp,
	}

	for name, cfg := range cs.current.SMTP {
		bundle.SMTP[name] = cfg
	}

	return bundle
}

func (cs *ConfigService) RefreshConfig(ctx context.Context) error {
	bundle, err := cs.LoadWithRetry(ctx)
	if err != nil {
		cs.mu.RLock()
		defer cs.mu.RUnlock()
		if cs.previous != nil {
			cs.logger.Warn("refresh failed, reverting to previous config",
				slog.Any("error", err),
			)
			cs.mu.RUnlock()
			cs.mu.Lock()
			cs.current = cs.previous
			cs.mu.Unlock()
		}
		return err
	}

	cs.Store(bundle)
	return nil
}

func (cs *ConfigService) GetRevision() int64 {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.revision
}

func (cs *ConfigService) GetCacheAge() time.Duration {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if cs.current == nil {
		return -1
	}

	return time.Since(cs.current.Timestamp)
}

type TransientError struct {
	msg string
}

func (e *TransientError) Error() string {
	return e.msg
}

func isTransientError(err error) bool {
	_, ok := err.(*TransientError)
	return ok
}
