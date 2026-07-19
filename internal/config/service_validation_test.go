package config

import (
	"log/slog"
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
		{"bad service name", strings.Replace(validServiceJSON, `"service": "billing-api",`, `"service": "Billing API",`, 1), "service"},
		{"enabled without keys", strings.Replace(validServiceJSON, `[{"id": "k1", "sha256": "`+testHash+`", "state": "active"}]`, "[]", 1), "keys"},
		{"default not in allowlist", strings.Replace(validServiceJSON, `"default_provider": "sendgrid"`, `"default_provider": "ses"`, 1), "default_provider"},
		{"bad sha256", strings.Replace(validServiceJSON, testHash, "nothex", 1), "sha256"},
		{"bad from address", strings.Replace(validServiceJSON, "billing@corp.com", "not-an-address", 1), "from.address"},
		{"zero rpm", strings.Replace(validServiceJSON, `"rpm": 60`, `"rpm": 0`, 1), "rate.rpm"},
		{"bad key id", strings.Replace(validServiceJSON, `"id": "k1"`, `"id": "K1!"`, 1), "keys[0].id"},
		{"unknown channel", strings.Replace(validServiceJSON, `"email": {`, `"sms": {`, 1), "unknown channel"},
		{"zero daily", strings.Replace(validServiceJSON, `"daily": 5000`, `"daily": 0`, 1), "rate.daily"},
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

func TestValidateBundleRefs(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		b := &ConfigBundle{
			Tenants: map[string]*TenantConfig{"payments": {Tenant: "payments"}},
			SMTP:    map[string]*SMTPClientConfig{"sendgrid": {Name: "sendgrid"}},
			Services: map[string]*ServiceConfig{
				"billing-api": {
					Service: "billing-api",
					Tenant:  "payments",
					Channels: map[string]*ChannelPolicy{
						"email": {Providers: []string{"sendgrid"}, DefaultProvider: "sendgrid"},
					},
				},
			},
		}
		if err := ValidateBundleRefs(b, slog.Default()); err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
	})

	t.Run("two unknown tenants aggregated", func(t *testing.T) {
		b := &ConfigBundle{
			Tenants: map[string]*TenantConfig{},
			Services: map[string]*ServiceConfig{
				"svc-a": {Service: "svc-a", Tenant: "ghost-a"},
				"svc-b": {Service: "svc-b", Tenant: "ghost-b"},
			},
		}
		err := ValidateBundleRefs(b, slog.Default())
		if err == nil {
			t.Fatal("expected error for unknown tenants")
		}
		if !strings.Contains(err.Error(), "svc-a") || !strings.Contains(err.Error(), "svc-b") {
			t.Fatalf("expected error to mention both service names, got: %v", err)
		}
	})

	t.Run("unknown provider is warn-only", func(t *testing.T) {
		b := &ConfigBundle{
			Tenants: map[string]*TenantConfig{"payments": {Tenant: "payments"}},
			SMTP:    map[string]*SMTPClientConfig{},
			Services: map[string]*ServiceConfig{
				"billing-api": {
					Service: "billing-api",
					Tenant:  "payments",
					Channels: map[string]*ChannelPolicy{
						"email": {Providers: []string{"unknown-provider"}, DefaultProvider: "unknown-provider"},
					},
				},
			},
		}
		if err := ValidateBundleRefs(b, slog.Default()); err != nil {
			t.Fatalf("expected nil error (warn-only), got: %v", err)
		}
	})

	t.Run("nil logger with unknown provider does not panic", func(t *testing.T) {
		b := &ConfigBundle{
			Tenants: map[string]*TenantConfig{"payments": {Tenant: "payments"}},
			SMTP:    map[string]*SMTPClientConfig{},
			Services: map[string]*ServiceConfig{
				"billing-api": {
					Service: "billing-api",
					Tenant:  "payments",
					Channels: map[string]*ChannelPolicy{
						"email": {Providers: []string{"unknown-provider"}, DefaultProvider: "unknown-provider"},
					},
				},
			},
		}
		if err := ValidateBundleRefs(b, nil); err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
	})
}

func TestBuildDevBundle_SynthesizesServiceAndTenant(t *testing.T) {
	t.Setenv("DEV_MODE", "true")
	t.Setenv("DEV_SMTP_HOST", "localhost")
	t.Setenv("DEV_SMTP_PORT", "2525")
	t.Setenv("DEV_SMTP_USERNAME", "u")
	t.Setenv("DEV_SMTP_PASSWORD", "p")
	t.Setenv("DEV_API_KEY", "bk_k1_devsecret")

	bundle, err := buildDevBundle()
	if err != nil {
		t.Fatalf("buildDevBundle: %v", err)
	}
	if len(bundle.Services) != 1 || bundle.Services["dev"] == nil {
		t.Fatalf("expected synthesized dev service, got %+v", bundle.Services)
	}
	svc := bundle.Services["dev"]
	if svc.Tenant != "dev" || !svc.Enabled {
		t.Fatalf("unexpected service: %+v", svc)
	}
	pol := svc.Channels["email"]
	if pol == nil || pol.DefaultProvider != "dev" || pol.Rate.RPM != 1000 || pol.Rate.Daily != 100000 {
		t.Fatalf("unexpected policy: %+v", pol)
	}
	if _, ok := bundle.Tenants["dev"]; !ok {
		t.Fatal("expected synthesized dev tenant")
	}
}

func TestBuildDevBundle_RequiresDevAPIKey(t *testing.T) {
	t.Setenv("DEV_SMTP_HOST", "localhost")
	t.Setenv("DEV_API_KEY", "")
	if _, err := buildDevBundle(); err == nil {
		t.Fatal("expected error when DEV_API_KEY unset")
	}
}
