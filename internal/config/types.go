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
	IsDefault   bool          `json:"is_default,omitempty"`
	FromAddress string        `json:"from_address,omitempty"`
	FromName    string        `json:"from_name,omitempty"`
}

// KeyEntry is one API key registered to a service. Only the SHA-256 of the
// full key (bk_<id>_<secret>) is stored; two active entries enable rotation.
type KeyEntry struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	State  string `json:"state"` // "active"
}

// FromIdentity is the policy-enforced sender identity for a service+channel.
type FromIdentity struct {
	Address string `json:"address"`
	Name    string `json:"name,omitempty"`
}

// RateConfig caps a service's throughput on one channel.
type RateConfig struct {
	RPM   int `json:"rpm"`
	Daily int `json:"daily"`
}

// ChannelPolicy is what a service may do on one channel.
type ChannelPolicy struct {
	Providers       []string      `json:"providers"`
	DefaultProvider string        `json:"default_provider"`
	From            *FromIdentity `json:"from,omitempty"`
	Rate            RateConfig    `json:"rate"`
}

// ServiceConfig is one registered calling service (control-plane doc).
type ServiceConfig struct {
	Service  string                    `json:"service"`
	Tenant   string                    `json:"tenant"`
	Enabled  bool                      `json:"enabled"`
	Keys     []KeyEntry                `json:"keys"`
	Channels map[string]*ChannelPolicy `json:"channels"`
}

// TenantConfig is tenant metadata (a team/product owning services).
type TenantConfig struct {
	Tenant string `json:"tenant"`
	Name   string `json:"name,omitempty"`
	Owner  string `json:"owner,omitempty"`
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
	SMTP      map[string]*SMTPClientConfig `json:"smtp"` // email providers
	Tenants   map[string]*TenantConfig     `json:"tenants"`
	Services  map[string]*ServiceConfig    `json:"services"`
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
