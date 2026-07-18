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

type SMTPClientConfig struct {
	Name        string        `json:"name"`
	Provider    string        `json:"provider"`
	Host        string        `json:"host"`
	Port        int           `json:"port"`
	Username    string        `json:"username"`
	Password    string        `json:"-"`
	AuthType    AuthType      `json:"auth_type"`
	TLS         TLSConfig     `json:"tls"`
	Timeout     time.Duration `json:"timeout"`
	Categories  []string      `json:"categories,omitempty"`
	IsDefault   bool          `json:"is_default,omitempty"`
	FromAddress string        `json:"from_address,omitempty"`
	FromName    string        `json:"from_name,omitempty"`
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

var (
	ErrProviderNotFound     = fmt.Errorf("provider not found")
	ErrConfigNotInitialized = fmt.Errorf("config service not initialized")
)

// Custom unmarshaler to handle string timeout and json:"-" fields
func (c *SMTPClientConfig) UnmarshalJSON(data []byte) error {
	type Alias SMTPClientConfig
	aux := &struct {
		Timeout  string `json:"timeout"`
		Password string `json:"password"`
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
	return nil
}
