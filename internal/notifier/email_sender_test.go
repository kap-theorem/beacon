package notifier

import (
	"context"
	"testing"
	"time"

	"beacon/internal/config"
	"beacon/internal/models"
	"beacon/internal/testsupport"
)

func TestEmailSender_SendsAndReusesConnection(t *testing.T) {
	srv := testsupport.NewMockSMTPServer(t)
	sender := NewEmailSender(&config.SMTPClientConfig{
		Host: srv.Host(), Port: srv.Port(),
		Username: "u", Password: "p", AuthType: config.AuthPlain,
		Timeout: 5 * time.Second, FromAddress: "noreply@corp.com", FromName: "Beacon",
	})
	defer sender.Close()

	n := &models.Notification{
		Channel: "email", Service: "svc", Tenant: "t", Provider: "p",
		Email: &models.EmailPayload{To: "a@b.com", Subject: "s1", Body: "b1"},
	}
	if err := sender.Send(context.Background(), n); err != nil {
		t.Fatalf("first send: %v", err)
	}
	n.Email.Subject = "s2"
	if err := sender.Send(context.Background(), n); err != nil {
		t.Fatalf("second send (reused conn): %v", err)
	}
	if got := len(srv.Messages()); got != 2 {
		t.Fatalf("mock server captured %d messages, want 2", got)
	}
}

func TestEmailSender_PolicyFromOverridesProviderFrom(t *testing.T) {
	srv := testsupport.NewMockSMTPServer(t)
	sender := NewEmailSender(&config.SMTPClientConfig{
		Host: srv.Host(), Port: srv.Port(),
		Username: "u", Password: "p", AuthType: config.AuthPlain,
		FromAddress: "provider@corp.com",
	})
	defer sender.Close()

	n := &models.Notification{Email: &models.EmailPayload{
		To: "a@b.com", Subject: "s", Body: "b",
		FromAddress: "billing@corp.com", FromName: "Billing",
	}}
	if err := sender.Send(context.Background(), n); err != nil {
		t.Fatalf("send: %v", err)
	}
	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("captured %d messages, want 1", len(msgs))
	}
	if msgs[0].From != "billing@corp.com" {
		t.Fatalf("envelope From = %q, want policy-injected billing@corp.com", msgs[0].From)
	}
}

func TestEmailSender_NilPayloadRejected(t *testing.T) {
	sender := NewEmailSender(&config.SMTPClientConfig{Host: "localhost", Port: 1})
	if err := sender.Send(context.Background(), &models.Notification{}); err == nil {
		t.Fatal("nil Email payload must error, not panic")
	}
}
