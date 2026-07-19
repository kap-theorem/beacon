package temporal

import (
	"context"
	"errors"
	"testing"

	"beacon/internal/models"
	"beacon/internal/notifier"
)

// captureSender is a test double for notifier.Sender that records the
// notification it was sent, and can be configured to return an error.
type captureSender struct {
	got *models.Notification
	err error
}

func (c *captureSender) Send(ctx context.Context, n *models.Notification) error {
	c.got = n
	return c.err
}

var _ notifier.Sender = (*captureSender)(nil)

func TestSendEmailActivity_NormalizesLegacyInput(t *testing.T) {
	cap := &captureSender{}
	a := &EmailActivities{GetSender: func() notifier.Sender { return cap }}
	legacy := &models.Notification{LegacyTo: "a@b.com", LegacySubject: "s", LegacyBody: "b"}
	if err := a.SendEmailActivity(context.Background(), legacy); err != nil {
		t.Fatalf("activity: %v", err)
	}
	if cap.got == nil || cap.got.Email == nil || cap.got.Email.To != "a@b.com" {
		t.Fatalf("legacy input not normalized: %+v", cap.got)
	}
}

func TestSendEmailActivity_RejectsEmptyPayload(t *testing.T) {
	a := &EmailActivities{GetSender: func() notifier.Sender { return &captureSender{} }}
	if err := a.SendEmailActivity(context.Background(), &models.Notification{}); err == nil {
		t.Fatal("empty notification must error")
	}
}

// TestSendEmailActivity_DelegatesToSender verifies that SendEmailActivity
// forwards an already-envelope-shaped notification to the underlying Sender
// with its fields intact.
func TestSendEmailActivity_DelegatesToSender(t *testing.T) {
	cs := &captureSender{}
	a := &EmailActivities{GetSender: func() notifier.Sender { return cs }}

	n := &models.Notification{Channel: "email", Email: &models.EmailPayload{To: "a@b.com", Subject: "s"}}
	if err := a.SendEmailActivity(context.Background(), n); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cs.got == nil {
		t.Fatal("sender was not called")
	}
	if cs.got.Email.To != "a@b.com" || cs.got.Email.Subject != "s" {
		t.Fatalf("unexpected payload: %+v", cs.got.Email)
	}
}

// TestSendEmailActivity_PropagatesSenderError verifies that errors returned
// by the Sender are surfaced unchanged by SendEmailActivity.
func TestSendEmailActivity_PropagatesSenderError(t *testing.T) {
	sentinel := errors.New("smtp down")
	cs := &captureSender{err: sentinel}
	a := &EmailActivities{GetSender: func() notifier.Sender { return cs }}

	n := &models.Notification{Channel: "email", Email: &models.EmailPayload{To: "x@y.com", Subject: "fail"}}
	err := a.SendEmailActivity(context.Background(), n)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
}
