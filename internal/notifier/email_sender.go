package notifier

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"

	"beacon/internal/config"
	"beacon/internal/models"

	gomail "gopkg.in/mail.v2"
)

// EmailSender delivers email notifications over SMTP, honoring the full
// provider config (TLS mode, server name, timeout). The SMTP connection is
// kept open between sends and re-dialed once on send failure.
type EmailSender struct {
	cfg    *config.SMTPClientConfig
	mu     sync.Mutex
	closer gomail.SendCloser
}

func NewEmailSender(cfg *config.SMTPClientConfig) *EmailSender {
	return &EmailSender{cfg: cfg}
}

func (e *EmailSender) dialer() *gomail.Dialer {
	d := gomail.NewDialer(e.cfg.Host, e.cfg.Port, e.cfg.Username, e.cfg.Password)
	if e.cfg.Timeout > 0 {
		d.Timeout = e.cfg.Timeout
	}
	if e.cfg.TLS.Enabled {
		d.TLSConfig = &tls.Config{ServerName: e.cfg.TLS.ServerName}
		if e.cfg.Port == 465 {
			d.SSL = true // implicit TLS
		} else {
			d.StartTLSPolicy = gomail.MandatoryStartTLS
		}
	}
	return d
}

func (e *EmailSender) Send(ctx context.Context, n *models.Notification) error {
	p := n.Email
	if p == nil {
		return fmt.Errorf("notification has no email payload")
	}

	m := gomail.NewMessage()
	fromAddr, fromName := e.cfg.FromAddress, e.cfg.FromName
	if p.FromAddress != "" {
		fromAddr, fromName = p.FromAddress, p.FromName
	}
	m.SetAddressHeader("From", fromAddr, fromName)
	m.SetHeader("To", p.To)
	if len(p.CC) > 0 {
		m.SetHeader("Cc", p.CC...)
	}
	if len(p.BCC) > 0 {
		m.SetHeader("Bcc", p.BCC...)
	}
	m.SetHeader("Subject", p.Subject)
	contentType := "text/plain"
	if p.HTML {
		contentType = "text/html"
	}
	m.SetBody(contentType, p.Body)

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closer == nil {
		c, err := e.dialer().Dial()
		if err != nil {
			return fmt.Errorf("smtp dial: %w", err)
		}
		e.closer = c
	}
	if err := gomail.Send(e.closer, m); err != nil {
		// Stale connection (server idle-closed, network blip): re-dial once.
		_ = e.closer.Close()
		e.closer = nil
		c, derr := e.dialer().Dial()
		if derr != nil {
			return fmt.Errorf("smtp re-dial after send failure (%v): %w", err, derr)
		}
		e.closer = c
		if err2 := gomail.Send(e.closer, m); err2 != nil {
			_ = e.closer.Close()
			e.closer = nil
			return fmt.Errorf("smtp send: %w", err2)
		}
	}
	return nil
}

// Close releases the kept-open SMTP connection.
func (e *EmailSender) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closer != nil {
		_ = e.closer.Close()
		e.closer = nil
	}
}
