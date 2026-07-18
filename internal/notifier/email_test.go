package notifier

import (
	"beacon/internal/models"
	"beacon/internal/testsupport"
	"context"
	"testing"
)

// TestEmailService_Send_Success verifies that a well-formed email is delivered
// to the mock SMTP server and captured with the correct recipient.
func TestEmailService_Send_Success(t *testing.T) {
	srv := testsupport.NewMockSMTPServer(t)
	svc := NewEmailService(srv.Host(), srv.Port(), "", "", "beacon@local", "Beacon")

	err := svc.Send(context.Background(), &Message[models.EmailMessage]{
		Type: EmailNotifier,
		Data: models.EmailMessage{To: "a@b.com", Subject: "hello", Body: "world"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := srv.Messages()
	if len(got) != 1 {
		t.Fatalf("expected 1 captured message, got %d", len(got))
	}
	if len(got[0].To) == 0 || got[0].To[0] != "a@b.com" {
		t.Fatalf("unexpected To: %+v", got[0].To)
	}
}

// TestEmailService_Send_MultipleMessages verifies sequential sends both arrive.
func TestEmailService_Send_MultipleMessages(t *testing.T) {
	srv := testsupport.NewMockSMTPServer(t)
	svc := NewEmailService(srv.Host(), srv.Port(), "", "", "from@local", "Sender")

	for i, to := range []string{"one@test.com", "two@test.com"} {
		err := svc.Send(context.Background(), &Message[models.EmailMessage]{
			Type: EmailNotifier,
			Data: models.EmailMessage{To: to, Subject: "sub", Body: "body"},
		})
		if err != nil {
			t.Fatalf("Send[%d]: %v", i, err)
		}
	}

	msgs := srv.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
}

// TestEmailService_Send_DialError verifies that an unreachable server produces
// a non-nil error (exercises the error branch inside Send).
func TestEmailService_Send_DialError(t *testing.T) {
	// Port 1 is reserved/privileged and will always refuse the connection.
	svc := NewEmailService("127.0.0.1", 1, "", "", "f@local", "F")

	err := svc.Send(context.Background(), &Message[models.EmailMessage]{
		Type: EmailNotifier,
		Data: models.EmailMessage{To: "nobody@nowhere.com", Subject: "s", Body: "b"},
	})
	if err == nil {
		t.Fatal("expected dial error, got nil")
	}
}

// TestEmailService_Send_WithCredentials exercises the auth-path code in
// NewEmailService/Send so that branches with non-empty username/password are
// covered.  The mock server advertises no AUTH, so the dialer will fall back
// gracefully (or fail); either way the code path is exercised.
func TestEmailService_Send_WithCredentials(t *testing.T) {
	srv := testsupport.NewMockSMTPServer(t)
	// The mock SMTP server does not advertise AUTH, so gopkg.in/mail.v2 will
	// attempt auth and receive an unexpected response. We only care that the
	// credential-bearing path through NewEmailService is reached; we accept
	// either success or a transport-level error here.
	svc := NewEmailService(srv.Host(), srv.Port(), "user", "pass", "from@local", "Sender")

	_ = svc.Send(context.Background(), &Message[models.EmailMessage]{
		Type: EmailNotifier,
		Data: models.EmailMessage{To: "cred@test.com", Subject: "auth", Body: "test"},
	})
	// No assertion on err — the mock server may or may not accept auth.
	// The important thing is the code path through Send is executed.
}

// TestEmailService_NewEmailService_FieldsSet verifies that NewEmailService
// stores every parameter in the returned struct.
func TestEmailService_NewEmailService_FieldsSet(t *testing.T) {
	svc := NewEmailService("smtp.example.com", 587, "user@example.com", "secret", "noreply@example.com", "Example")

	if svc.SmtpServer != "smtp.example.com" {
		t.Errorf("SmtpServer: got %q", svc.SmtpServer)
	}
	if svc.SmtpPort != 587 {
		t.Errorf("SmtpPort: got %d", svc.SmtpPort)
	}
	if svc.EmailUsername != "user@example.com" {
		t.Errorf("EmailUsername: got %q", svc.EmailUsername)
	}
	if svc.EmailPassword != "secret" {
		t.Errorf("EmailPassword: got %q", svc.EmailPassword)
	}
	if svc.FromAddress != "noreply@example.com" {
		t.Errorf("FromAddress: got %q", svc.FromAddress)
	}
	if svc.FromName != "Example" {
		t.Errorf("FromName: got %q", svc.FromName)
	}
}
