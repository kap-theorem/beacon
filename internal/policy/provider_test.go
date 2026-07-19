package policy

import (
	"testing"

	"beacon/internal/config"
)

func pol() *config.ChannelPolicy {
	return &config.ChannelPolicy{
		Providers:       []string{"sendgrid", "ses"},
		DefaultProvider: "sendgrid",
	}
}

func TestResolveProvider_DefaultWhenEmpty(t *testing.T) {
	p, err := ResolveProvider(pol(), "")
	if err != nil || p != "sendgrid" {
		t.Fatalf("want sendgrid, got %q err=%v", p, err)
	}
}

func TestResolveProvider_AllowlistedRequest(t *testing.T) {
	p, err := ResolveProvider(pol(), "ses")
	if err != nil || p != "ses" {
		t.Fatalf("want ses, got %q err=%v", p, err)
	}
}

func TestResolveProvider_Forbidden(t *testing.T) {
	if _, err := ResolveProvider(pol(), "mailchimp"); err == nil {
		t.Fatal("provider outside allowlist must be rejected")
	}
}
