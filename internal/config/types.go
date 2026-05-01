package config

import (
	"encoding/json"
	"fmt"
	"time"
)

type AuthType string

const (
	AuthPlain  AuthType = "PLAIN"
	AuthLogin  AuthType = "LOGIN"
	AuthOAuth2 AuthType = "OAUTH2"
)

type TLSConfig struct {
	Enabled    bool   `json:"enabled"`
	ServerName string `json:"server_name,omitempty"`
}

type RuleSet struct {
	DailyLimit  int `json:"daily_limit,omitempty"`
	HourlyLimit int `json:"hourly_limit,omitempty"`
}

type SMTPClientConfig struct {
	Name       string        `json:"name"`
	Provider   string        `json:"provider"`
	Host       string        `json:"host"`
	Port       int           `json:"port"`
	Username   string        `json:"username"`
	Password   string        `json:"-"`
	APIKey     string        `json:"-"`
	AuthType   AuthType      `json:"auth_type"`
	TLS        TLSConfig     `json:"tls"`
	Timeout    time.Duration `json:"timeout"`
	MaxRetries int           `json:"max_retries"`
	MaxPerHour int           `json:"max_per_hour"`
	Rules      RuleSet       `json:"rules,omitempty"`
}

func (c *SMTPClientConfig) String() string {
	return fmt.Sprintf("SMTPClientConfig{Provider: %s, Host: %s:%d, AuthType: %s}",
		c.Provider, c.Host, c.Port, c.AuthType)
}

func (c *SMTPClientConfig) LogSafe() map[string]interface{} {
	return map[string]interface{}{
		"name":      c.Name,
		"provider":  c.Provider,
		"host":      c.Host,
		"port":      c.Port,
		"username":  c.Username,
		"auth_type": c.AuthType,
		"tls":       c.TLS,
		"timeout":   c.Timeout,
		"rules":     c.Rules,
	}
}

type FieldError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
	Value  string `json:"value,omitempty"`
}

type ValidationResult struct {
	Errors []FieldError `json:"errors"`
	Valid  bool         `json:"valid"`
}

type ConfigBundle struct {
	SMTP      map[string]*SMTPClientConfig `json:"smtp"`
	Revision  int64                        `json:"revision"`
	Timestamp time.Time                    `json:"timestamp"`
}

type ProviderInfo struct {
	Name string
	Path string
}

type ConfigLoadedCallback func(bundle *ConfigBundle, err error)

var (
	ErrProviderNotFound    = fmt.Errorf("provider not found")
	ErrConfigNotInitialized = fmt.Errorf("config service not initialized")
)

// Custom unmarshaler to handle string timeout and json:"-" fields
func (c *SMTPClientConfig) UnmarshalJSON(data []byte) error {
	type Alias SMTPClientConfig
	aux := &struct {
		Timeout string `json:"timeout"`
		Password string `json:"password"`
		APIKey  string `json:"api_key"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.Timeout != "" {
		dur, err := time.ParseDuration(aux.Timeout)
		if err != nil {
			return fmt.Errorf("invalid timeout format: %w", err)
		}
		c.Timeout = dur
	}
	if aux.Password != "" {
		c.Password = aux.Password
	}
	if aux.APIKey != "" {
		c.APIKey = aux.APIKey
	}
	return nil
}
