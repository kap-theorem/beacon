package config

import (
	"strings"
	"testing"
)

const validServiceJSON = `{
  "service": "billing-api",
  "tenant": "payments",
  "enabled": true,
  "keys": [{"id": "k1", "sha256": "` + testHash + `", "state": "active"}],
  "channels": {
    "email": {
      "providers": ["sendgrid"],
      "default_provider": "sendgrid",
      "from": {"address": "billing@corp.com", "name": "Billing"},
      "rate": {"rpm": 60, "daily": 5000}
    }
  }
}`

// 64 hex chars (sha256 of anything).
const testHash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func TestValidateServiceConfig_Valid(t *testing.T) {
	svc, err := ValidateServiceConfig(validServiceJSON)
	if err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
	if svc.Service != "billing-api" || svc.Tenant != "payments" {
		t.Fatalf("unexpected: %+v", svc)
	}
	if svc.Channels["email"].DefaultProvider != "sendgrid" {
		t.Fatalf("default provider not parsed")
	}
}

func TestValidateServiceConfig_Rejections(t *testing.T) {
	cases := []struct{ name, mutate, wantSubstr string }{
		{"missing tenant", strings.Replace(validServiceJSON, `"tenant": "payments",`, "", 1), "tenant"},
		{"enabled without keys", strings.Replace(validServiceJSON, `[{"id": "k1", "sha256": "`+testHash+`", "state": "active"}]`, "[]", 1), "keys"},
		{"default not in allowlist", strings.Replace(validServiceJSON, `"default_provider": "sendgrid"`, `"default_provider": "ses"`, 1), "default_provider"},
		{"bad sha256", strings.Replace(validServiceJSON, testHash, "nothex", 1), "sha256"},
		{"bad from address", strings.Replace(validServiceJSON, "billing@corp.com", "not-an-address", 1), "from.address"},
		{"zero rpm", strings.Replace(validServiceJSON, `"rpm": 60`, `"rpm": 0`, 1), "rate.rpm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateServiceConfig(tc.mutate)
			if err == nil || !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("want error containing %q, got %v", tc.wantSubstr, err)
			}
		})
	}
}

func TestValidateTenantConfig(t *testing.T) {
	if _, err := ValidateTenantConfig(`{"tenant": "payments", "name": "Payments"}`); err != nil {
		t.Fatalf("expected valid: %v", err)
	}
	if _, err := ValidateTenantConfig(`{"name": "no id"}`); err == nil {
		t.Fatal("expected rejection for missing tenant id")
	}
}

func TestValidateConfig_RejectsOAuth2(t *testing.T) {
	raw := `{"name":"x","provider":"x","host":"smtp.example.com","port":587,
		"username":"u","password":"p","auth_type":"OAUTH2"}`
	if _, err := ValidateConfig(raw); err == nil {
		t.Fatal("OAUTH2 must be rejected at validation until implemented")
	}
}
