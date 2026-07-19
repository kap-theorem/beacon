package notifier

import (
	"testing"

	"beacon/internal/config"
)

func TestProviderRegistry_ExistsAndNames(t *testing.T) {
	bundle := &config.ConfigBundle{SMTP: map[string]*config.SMTPClientConfig{
		"sendgrid": {Name: "sendgrid"},
		"ses":      {Name: "ses"},
	}}
	r := NewProviderRegistry(bundle)
	if !r.Exists("email", "sendgrid") {
		t.Fatal("sendgrid should exist on email channel")
	}
	if r.Exists("email", "mailchimp") || r.Exists("sms", "sendgrid") {
		t.Fatal("unknown provider/channel must not exist")
	}
	names := r.Names("email")
	if len(names) != 2 || names[0] != "sendgrid" || names[1] != "ses" {
		t.Fatalf("names: %v", names)
	}
}

func TestProviderRegistry_Reload(t *testing.T) {
	r := NewProviderRegistry(&config.ConfigBundle{SMTP: map[string]*config.SMTPClientConfig{"a": {Name: "a"}}})
	r.Reload(&config.ConfigBundle{SMTP: map[string]*config.SMTPClientConfig{"b": {Name: "b"}}})
	if r.Exists("email", "a") || !r.Exists("email", "b") {
		t.Fatal("reload must swap the provider set")
	}
}
