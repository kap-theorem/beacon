package notifier

import (
	"beacon/internal/models"
	"context"
)

type EmailService struct {
	// TODO: smtp server details
	smtp string
}

// NewEmailService creates a new EmailService instance with the specified SMTP server.
func NewEmailService(smtpServer string) *EmailService {
	return &EmailService{
		smtp: smtpServer,
	}
}

// Send sends an email using the configured SMTP server.
func (e *EmailService) Send(ctx context.Context, msg *Message[models.EmailMessage]) error {
	// TODO: implement email sending logic using smtp server
	println("Sending Email:")
	println("To:", msg.Data.To)
	println("Subject:", msg.Data.Subject)
	println("Body:", msg.Data.Body)

	return nil
}
