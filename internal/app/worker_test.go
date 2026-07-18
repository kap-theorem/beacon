package app

import (
	"errors"
	"testing"

	"beacon/internal/config"
)

func smtpCfg(name string, isDefault bool) *config.SMTPClientConfig {
	return &config.SMTPClientConfig{
		Name:      name,
		Provider:  name,
		Host:      "smtp.example.com",
		Port:      587,
		IsDefault: isDefault,
	}
}

func TestResolveWorkerProvider_NilBundle_ReturnsError(t *testing.T) {
	_, _, err := ResolveWorkerProvider(nil, "")
	if err == nil {
		t.Fatal("expected error for nil bundle, got nil")
	}
}

func TestResolveWorkerProvider_EmptySMTPMap_ReturnsError(t *testing.T) {
	bundle := &config.ConfigBundle{
		SMTP: map[string]*config.SMTPClientConfig{},
	}
	_, _, err := ResolveWorkerProvider(bundle, "")
	if err == nil {
		t.Fatal("expected error for empty SMTP map, got nil")
	}
}

func TestResolveWorkerProvider_ExplicitProvider_Found(t *testing.T) {
	bundle := &config.ConfigBundle{
		SMTP: map[string]*config.SMTPClientConfig{
			"sendgrid": smtpCfg("sendgrid", false),
			"mailgun":  smtpCfg("mailgun", false),
		},
	}
	name, cfg, err := ResolveWorkerProvider(bundle, "sendgrid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "sendgrid" {
		t.Errorf("expected name 'sendgrid', got %q", name)
	}
	if cfg == nil {
		t.Error("expected non-nil config")
	}
}

func TestResolveWorkerProvider_ExplicitProvider_Missing(t *testing.T) {
	bundle := &config.ConfigBundle{
		SMTP: map[string]*config.SMTPClientConfig{
			"sendgrid": smtpCfg("sendgrid", false),
		},
	}
	_, _, err := ResolveWorkerProvider(bundle, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing provider, got nil")
	}
	if !errors.Is(err, config.ErrProviderNotFound) {
		t.Errorf("expected errors.Is(err, ErrProviderNotFound), got: %v", err)
	}
}

func TestResolveWorkerProvider_DefaultProvider_SelectedAmongSeveral(t *testing.T) {
	bundle := &config.ConfigBundle{
		SMTP: map[string]*config.SMTPClientConfig{
			"sendgrid": smtpCfg("sendgrid", false),
			"mailgun":  smtpCfg("mailgun", true), // is_default
			"ses":      smtpCfg("ses", false),
		},
	}
	name, cfg, err := ResolveWorkerProvider(bundle, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "mailgun" {
		t.Errorf("expected default provider 'mailgun', got %q", name)
	}
	if cfg == nil {
		t.Error("expected non-nil config")
	}
}

func TestResolveWorkerProvider_SingleProvider_AutoSelected(t *testing.T) {
	bundle := &config.ConfigBundle{
		SMTP: map[string]*config.SMTPClientConfig{
			"solo": smtpCfg("solo", false),
		},
	}
	name, cfg, err := ResolveWorkerProvider(bundle, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "solo" {
		t.Errorf("expected 'solo', got %q", name)
	}
	if cfg == nil {
		t.Error("expected non-nil config")
	}
}

func TestResolveWorkerProvider_MultipleProviders_NoDefault_ReturnsError(t *testing.T) {
	bundle := &config.ConfigBundle{
		SMTP: map[string]*config.SMTPClientConfig{
			"alpha": smtpCfg("alpha", false),
			"beta":  smtpCfg("beta", false),
		},
	}
	_, _, err := ResolveWorkerProvider(bundle, "")
	if err == nil {
		t.Fatal("expected error for multiple providers without default, got nil")
	}
}
