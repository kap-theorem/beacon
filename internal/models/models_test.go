package models

import (
	"encoding/json"
	"testing"
)

func TestNormalize_LegacyEmailMessagePayload(t *testing.T) {
	// A pre-v2 SendEmailWorkflow input decoded into Notification.
	raw := `{"to":"a@b.com","subject":"hi","body":"text","client_hint":"transactional"}`
	var n Notification
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	n.Normalize()
	if n.Email == nil {
		t.Fatal("expected Email payload synthesized from legacy fields")
	}
	if n.Email.To != "a@b.com" || n.Email.Subject != "hi" || n.Email.Body != "text" {
		t.Fatalf("unexpected payload: %+v", n.Email)
	}
	if n.Channel != "email" {
		t.Fatalf("expected channel defaulted to email, got %q", n.Channel)
	}
}

func TestNormalize_NoopWhenEmailSet(t *testing.T) {
	n := Notification{Channel: "email", Email: &EmailPayload{To: "x@y.com", Subject: "s"}}
	n.Normalize()
	if n.Email.To != "x@y.com" {
		t.Fatalf("Normalize must not clobber an existing payload")
	}
}
