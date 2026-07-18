package temporal

import (
	"beacon/internal/models"
	"beacon/internal/notifier"
	"context"
	"errors"
	"testing"
)

// fakeNotifier is a test double for notifier.Notifier[models.EmailMessage].
type fakeNotifier struct {
	err error
	got *notifier.Message[models.EmailMessage]
}

func (f *fakeNotifier) Send(_ context.Context, m *notifier.Message[models.EmailMessage]) error {
	f.got = m
	return f.err
}

// TestSendEmailActivity_DelegatesToNotifier verifies that SendEmailActivity
// forwards the message to the underlying Notifier with the correct Data fields.
func TestSendEmailActivity_DelegatesToNotifier(t *testing.T) {
	fn := &fakeNotifier{}
	a := &EmailActivities{GetService: func() notifier.Notifier[models.EmailMessage] { return fn }}

	err := a.SendEmailActivity(context.Background(), &models.EmailMessage{To: "a@b.com", Subject: "s"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fn.got == nil {
		t.Fatal("notifier was not called")
	}
	if fn.got.Data.To != "a@b.com" {
		t.Fatalf("unexpected To: got %q, want %q", fn.got.Data.To, "a@b.com")
	}
	if fn.got.Data.Subject != "s" {
		t.Fatalf("unexpected Subject: got %q, want %q", fn.got.Data.Subject, "s")
	}
	if fn.got.Type != notifier.EmailNotifier {
		t.Fatalf("unexpected Type: got %q, want %q", fn.got.Type, notifier.EmailNotifier)
	}
	if fn.got.ID == "" {
		t.Fatal("expected a non-empty message ID")
	}
}

// TestSendEmailActivity_PropagatesNotifierError verifies that errors returned
// by the Notifier are surfaced unchanged by SendEmailActivity.
func TestSendEmailActivity_PropagatesNotifierError(t *testing.T) {
	sentinel := errors.New("smtp down")
	fn := &fakeNotifier{err: sentinel}
	a := &EmailActivities{GetService: func() notifier.Notifier[models.EmailMessage] { return fn }}

	err := a.SendEmailActivity(context.Background(), &models.EmailMessage{To: "x@y.com", Subject: "fail"})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
}
