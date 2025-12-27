package notifier

import (
	"beacon/internal/models"
	"context"
	"log"

	gomail "gopkg.in/mail.v2"
)

// EmailNotifier represents the email notification channel.
var EmailNotifier NotifierType = "email"

// EmailService implements the Notifier interface for sending emails.
type EmailService struct {
	SmtpServer    string
	SmtpPort      int
	EmailUsername string
	EmailPassword string
}

// NewEmailService creates a new EmailService instance with the specified SMTP server.
func NewEmailService(smtpServer string, port int, username, password string) *EmailService {
	return &EmailService{
		SmtpServer:    smtpServer,
		SmtpPort:      port,
		EmailUsername: username,
		EmailPassword: password,
	}
}

// Send sends an email using the configured SMTP server.
func (e *EmailService) Send(ctx context.Context, msg *Message[models.EmailMessage]) error {

	log.Println("Sending email to", msg.Data.To)

	// new mail message
	mail := gomail.NewMessage()
	mail.SetAddressHeader("From", e.EmailUsername, "kaplabs")
	mail.SetHeader("To", msg.Data.To)
	mail.SetHeader("Subject", msg.Data.Subject)
	mail.SetBody("text/plain", msg.Data.Body)

	// dial smtp server and send the email
	dialer := gomail.NewDialer(e.SmtpServer, e.SmtpPort, e.EmailUsername, e.EmailPassword)
	if err := dialer.DialAndSend(mail); err != nil {
		log.Println("Failed to send email to", msg.Data.To, ":", err)
		return err
	}
	log.Println("Email sent successfully to", msg.Data.To)
	return nil
}
