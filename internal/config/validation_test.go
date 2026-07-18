package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// validBase returns a minimal valid JSON string for mutation tests.
func validBaseJSON() string {
	cfg := map[string]interface{}{
		"name":      "test-provider",
		"provider":  "mailgun",
		"host":      "smtp.mailgun.org",
		"port":      587,
		"username":  "user@example.com",
		"password":  "secret",
		"auth_type": "PLAIN",
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}

// patchJSON returns a new JSON string with the given key set to value (or removed if value is nil).
func patchJSON(base string, key string, value interface{}) string {
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(base), &m)
	if value == nil {
		delete(m, key)
	} else {
		m[key] = value
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestValidateConfig_InvalidJSON(t *testing.T) {
	_, err := ValidateConfig("{not valid json")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "validation error") {
		t.Errorf("expected 'validation error' in error message, got: %v", err)
	}
}

func TestValidateConfig_MissingRequiredFields(t *testing.T) {
	base := validBaseJSON()
	tests := []struct {
		name    string
		json    string
		wantErr string
	}{
		{
			name:    "missing name",
			json:    patchJSON(base, "name", nil),
			wantErr: "name",
		},
		{
			name:    "missing provider",
			json:    patchJSON(base, "provider", nil),
			wantErr: "provider",
		},
		{
			name:    "missing host",
			json:    patchJSON(base, "host", nil),
			wantErr: "host",
		},
		{
			name:    "missing port",
			json:    patchJSON(base, "port", 0), // 0 means absent
			wantErr: "port",
		},
		{
			name:    "missing auth_type",
			json:    patchJSON(base, "auth_type", nil),
			wantErr: "auth_type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateConfig(tt.json)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error to mention %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidateConfig_SemanticFailures(t *testing.T) {
	base := validBaseJSON()
	tests := []struct {
		name    string
		json    string
		wantErr string
	}{
		{
			name:    "bad host",
			json:    patchJSON(base, "host", "not a valid host!!"),
			wantErr: "host",
		},
		{
			name:    "port too low",
			json:    patchJSON(base, "port", -1),
			wantErr: "port",
		},
		{
			name:    "port too high",
			json:    patchJSON(base, "port", 99999),
			wantErr: "port",
		},
		{
			name:    "bad auth_type",
			json:    patchJSON(base, "auth_type", "BADAUTH"),
			wantErr: "auth_type",
		},
		{
			name: "missing username for non-OAUTH2",
			json: patchJSON(patchJSON(base, "auth_type", "PLAIN"), "username", nil),
			// Note: "username" will be the error field
			wantErr: "username",
		},
		{
			name: "missing password and api_key",
			json: func() string {
				j := patchJSON(base, "password", nil)
				// Remove api_key too (not in base, but ensure no api_key)
				var m map[string]interface{}
				_ = json.Unmarshal([]byte(j), &m)
				delete(m, "api_key")
				delete(m, "password")
				b, _ := json.Marshal(m)
				return string(b)
			}(),
			wantErr: "password",
		},
		{
			name: "TLS enabled without server_name",
			json: func() string {
				var m map[string]interface{}
				_ = json.Unmarshal([]byte(base), &m)
				m["tls"] = map[string]interface{}{"enabled": true}
				b, _ := json.Marshal(m)
				return string(b)
			}(),
			wantErr: "tls.server_name",
		},
		{
			name:    "negative max_retries",
			json:    patchJSON(base, "max_retries", -1),
			wantErr: "max_retries",
		},
		{
			name:    "negative max_per_hour",
			json:    patchJSON(base, "max_per_hour", -1),
			wantErr: "max_per_hour",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateConfig(tt.json)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error to mention %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidateConfig_NegativeTimeoutSemantic(t *testing.T) {
	// Timeout is a string duration in JSON; a negative duration parses fine
	// and triggers the semantic check.
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(validBaseJSON()), &m)
	m["timeout"] = "-1s"
	b, _ := json.Marshal(m)
	_, err := ValidateConfig(string(b))
	if err == nil {
		t.Fatal("expected error for negative timeout, got nil")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected error to mention 'timeout', got: %v", err)
	}
}

func TestValidateConfig_DefaultTimeout(t *testing.T) {
	// When timeout is absent (zero), ValidateConfig should apply 30s default.
	cfg, err := ValidateConfig(validBaseJSON())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Timeout != 30*1e9*30 { // 30 * time.Second
		// check via string representation to avoid importing time at expression level
	}
	if cfg.Timeout.Seconds() != 30 {
		t.Errorf("expected default timeout 30s, got %v", cfg.Timeout)
	}
}

func TestValidateConfig_ValidConfig(t *testing.T) {
	cfg, err := ValidateConfig(validBaseJSON())
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if cfg.Name != "test-provider" {
		t.Errorf("expected name 'test-provider', got %q", cfg.Name)
	}
	if cfg.Host != "smtp.mailgun.org" {
		t.Errorf("expected host 'smtp.mailgun.org', got %q", cfg.Host)
	}
}

func TestValidateConfig_OAUTH2NoUsernameRequired(t *testing.T) {
	base := validBaseJSON()
	j := patchJSON(base, "auth_type", "OAUTH2")
	j = patchJSON(j, "username", nil)
	// OAuth2 requires api_key or password; keep password in base.
	_, err := ValidateConfig(j)
	if err != nil {
		t.Fatalf("OAUTH2 without username should be valid, got: %v", err)
	}
}

func TestIsValidHost(t *testing.T) {
	tests := []struct {
		host  string
		valid bool
	}{
		{"smtp.example.com", true},
		{"localhost", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"192.168.1.1", true},
		{"sub.domain.example.com", true},
		{"", false},
		{"not valid host!!", false},
		{"has space .com", false},
		{"-startswith.dash.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			got := isValidHost(tt.host)
			if got != tt.valid {
				t.Errorf("isValidHost(%q) = %v, want %v", tt.host, got, tt.valid)
			}
		})
	}
}

func TestValidationResult_Error(t *testing.T) {
	vr := &ValidationResult{
		Valid: false,
		Errors: []FieldError{
			{Field: "name", Reason: "required"},
			{Field: "host", Reason: "invalid DNS name or IP address", Value: "bad-host!!"},
		},
	}
	got := vr.Error()
	if !strings.Contains(got, "validation errors") {
		t.Errorf("expected 'validation errors' prefix, got: %q", got)
	}
	if !strings.Contains(got, "name") {
		t.Errorf("expected 'name' in error, got: %q", got)
	}
	if !strings.Contains(got, "host") {
		t.Errorf("expected 'host' in error, got: %q", got)
	}
}

func TestValidationResult_ErrorWhenValid(t *testing.T) {
	vr := &ValidationResult{Valid: true}
	got := vr.Error()
	if got != "" {
		t.Errorf("expected empty string for valid result, got: %q", got)
	}
}

func TestValidateConfig_TLSWithServerName(t *testing.T) {
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(validBaseJSON()), &m)
	m["tls"] = map[string]interface{}{
		"enabled":     true,
		"server_name": "smtp.mailgun.org",
	}
	b, _ := json.Marshal(m)
	_, err := ValidateConfig(string(b))
	if err != nil {
		t.Errorf("TLS with server_name should be valid, got: %v", err)
	}
}

func TestValidateConfig_InvalidTimeoutFormat(t *testing.T) {
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(validBaseJSON()), &m)
	m["timeout"] = "not-a-duration"
	b, _ := json.Marshal(m)
	_, err := ValidateConfig(string(b))
	if err == nil {
		t.Fatal("expected error for invalid timeout format, got nil")
	}
}
