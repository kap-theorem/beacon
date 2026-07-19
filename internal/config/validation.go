package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"
)

func ValidateConfig(rawJSON string) (*SMTPClientConfig, error) {
	var cfg SMTPClientConfig

	if err := json.Unmarshal([]byte(rawJSON), &cfg); err != nil {
		vr := &ValidationResult{
			Errors: []FieldError{
				{Field: "json", Reason: "invalid JSON", Value: rawJSON[:min(50, len(rawJSON))]},
			},
		}
		return nil, fmt.Errorf("validation error: %w", vr)
	}

	result := validateStructural(&cfg)
	if !result.Valid {
		return nil, fmt.Errorf("validation error: %w", result)
	}

	result = validateSemantic(&cfg)
	if !result.Valid {
		return nil, fmt.Errorf("validation error: %w", result)
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	return &cfg, nil
}

func (vr *ValidationResult) Error() string {
	if vr.Valid {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("validation errors: ")
	for _, err := range vr.Errors {
		fmt.Fprintf(&sb, "[%s: %s] ", err.Field, err.Reason)
	}
	return sb.String()
}

// ValidationResult implements the error interface
var _ error = (*ValidationResult)(nil)

func validateStructural(cfg *SMTPClientConfig) *ValidationResult {
	var errors []FieldError

	if cfg.Name == "" {
		errors = append(errors, FieldError{Field: "name", Reason: "required"})
	}
	if cfg.Provider == "" {
		errors = append(errors, FieldError{Field: "provider", Reason: "required"})
	}
	if cfg.Host == "" {
		errors = append(errors, FieldError{Field: "host", Reason: "required"})
	}
	if cfg.Port == 0 {
		errors = append(errors, FieldError{Field: "port", Reason: "required"})
	}
	if cfg.AuthType == "" {
		errors = append(errors, FieldError{Field: "auth_type", Reason: "required"})
	}

	if len(errors) > 0 {
		return &ValidationResult{Errors: errors, Valid: false}
	}

	return &ValidationResult{Valid: true}
}

func validateSemantic(cfg *SMTPClientConfig) *ValidationResult {
	var errors []FieldError

	if !isValidHost(cfg.Host) {
		errors = append(errors, FieldError{
			Field:  "host",
			Reason: "invalid DNS name or IP address",
			Value:  cfg.Host,
		})
	}

	if cfg.Port < 1 || cfg.Port > 65535 {
		errors = append(errors, FieldError{
			Field:  "port",
			Reason: "must be between 1 and 65535",
			Value:  fmt.Sprintf("%d", cfg.Port),
		})
	}

	if cfg.AuthType != AuthPlain && cfg.AuthType != AuthLogin {
		reason := fmt.Sprintf("must be one of %v", []AuthType{AuthPlain, AuthLogin})
		if cfg.AuthType == AuthOAuth2 {
			reason = "OAUTH2 is not implemented; configure PLAIN or LOGIN"
		}
		errors = append(errors, FieldError{Field: "auth_type", Reason: reason, Value: string(cfg.AuthType)})
	}

	if cfg.Username == "" {
		errors = append(errors, FieldError{
			Field:  "username",
			Reason: "required for non-OAuth2 auth",
		})
	}

	if cfg.Password == "" {
		errors = append(errors, FieldError{
			Field:  "password",
			Reason: "required",
		})
	}

	if cfg.TLS.Enabled && cfg.TLS.ServerName == "" {
		errors = append(errors, FieldError{
			Field:  "tls.server_name",
			Reason: "required when TLS is enabled",
		})
	}

	if cfg.Timeout < 0 {
		errors = append(errors, FieldError{
			Field:  "timeout",
			Reason: "must be >= 0",
			Value:  cfg.Timeout.String(),
		})
	}

	if len(errors) > 0 {
		return &ValidationResult{Errors: errors, Valid: false}
	}

	return &ValidationResult{Valid: true}
}

func isValidHost(host string) bool {
	if host == "" {
		return false
	}

	if net.ParseIP(host) != nil {
		return true
	}

	dnsRegex := regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)
	return dnsRegex.MatchString(host) || host == "localhost"
}

var sha256HexRe = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var keyIDRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// ValidateTenantConfig parses and validates one /beacon/tenants secret.
func ValidateTenantConfig(rawJSON string) (*TenantConfig, error) {
	var t TenantConfig
	if err := json.Unmarshal([]byte(rawJSON), &t); err != nil {
		return nil, fmt.Errorf("tenant config: invalid JSON: %w", err)
	}
	if t.Tenant == "" {
		return nil, fmt.Errorf("tenant config: field tenant is required")
	}
	return &t, nil
}

// ValidateServiceConfig parses and validates one /beacon/services secret.
// Cross-references (tenant/provider existence) are checked by ValidateBundleRefs.
func ValidateServiceConfig(rawJSON string) (*ServiceConfig, error) {
	var s ServiceConfig
	if err := json.Unmarshal([]byte(rawJSON), &s); err != nil {
		return nil, fmt.Errorf("service config: invalid JSON: %w", err)
	}
	var errs []FieldError
	if s.Service == "" {
		errs = append(errs, FieldError{Field: "service", Reason: "required"})
	}
	if s.Tenant == "" {
		errs = append(errs, FieldError{Field: "tenant", Reason: "required"})
	}
	active := 0
	for i, k := range s.Keys {
		if !keyIDRe.MatchString(k.ID) {
			errs = append(errs, FieldError{Field: fmt.Sprintf("keys[%d].id", i), Reason: "must match ^[a-z0-9-]{1,32}$", Value: k.ID})
		}
		if !sha256HexRe.MatchString(k.SHA256) {
			errs = append(errs, FieldError{Field: fmt.Sprintf("keys[%d].sha256", i), Reason: "must be 64 hex chars"})
		}
		if k.State == "active" {
			active++
		}
	}
	if s.Enabled && active == 0 {
		errs = append(errs, FieldError{Field: "keys", Reason: "enabled service needs at least one active key"})
	}
	for chName, pol := range s.Channels {
		if chName != "email" {
			errs = append(errs, FieldError{Field: "channels", Reason: "unknown channel", Value: chName})
			continue
		}
		if pol == nil || len(pol.Providers) == 0 {
			errs = append(errs, FieldError{Field: chName + ".providers", Reason: "required, non-empty"})
			continue
		}
		if !slices.Contains(pol.Providers, pol.DefaultProvider) {
			errs = append(errs, FieldError{Field: chName + ".default_provider", Reason: "must be in providers list", Value: pol.DefaultProvider})
		}
		if pol.From != nil {
			if _, err := mail.ParseAddress(pol.From.Address); err != nil {
				errs = append(errs, FieldError{Field: chName + ".from.address", Reason: "invalid email address", Value: pol.From.Address})
			}
		}
		if pol.Rate.RPM < 1 {
			errs = append(errs, FieldError{Field: chName + ".rate.rpm", Reason: "must be >= 1"})
		}
		if pol.Rate.Daily < 1 {
			errs = append(errs, FieldError{Field: chName + ".rate.daily", Reason: "must be >= 1"})
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("validation error: %w", &ValidationResult{Errors: errs})
	}
	return &s, nil
}

// ValidateBundleRefs enforces cross-references after a full bundle is loaded:
// unknown tenant -> reject; unknown provider in an allowlist -> warn only
// (providers hot-reload separately; requests bound to a missing provider 503).
func ValidateBundleRefs(b *ConfigBundle, logger *slog.Logger) error {
	for name, svc := range b.Services {
		if _, ok := b.Tenants[svc.Tenant]; !ok {
			return fmt.Errorf("service %q references unknown tenant %q", name, svc.Tenant)
		}
		for chName, pol := range svc.Channels {
			for _, p := range pol.Providers {
				if _, ok := b.SMTP[p]; !ok {
					logger.Warn("service references unknown provider",
						slog.String("service", name), slog.String("channel", chName), slog.String("provider", p))
				}
			}
		}
	}
	return nil
}
