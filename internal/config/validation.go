package config

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
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

	if cfg.AuthType != AuthPlain && cfg.AuthType != AuthLogin && cfg.AuthType != AuthOAuth2 {
		errors = append(errors, FieldError{
			Field:  "auth_type",
			Reason: fmt.Sprintf("must be one of %v", []AuthType{AuthPlain, AuthLogin, AuthOAuth2}),
			Value:  string(cfg.AuthType),
		})
	}

	if cfg.AuthType != AuthOAuth2 && cfg.Username == "" {
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
