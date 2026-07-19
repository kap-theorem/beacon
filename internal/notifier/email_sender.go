package notifier

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"

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

var _ Sender = (*EmailSender)(nil)

func NewEmailSender(cfg *config.SMTPClientConfig) *EmailSender {
	return &EmailSender{cfg: cfg}
}

// transportError reports whether err looks like a broken/stale connection
// (worth one re-dial) rather than a protocol-level SMTP rejection, which
// would fail identically on retry and, worse, can double-deliver a message
// whose DATA was already accepted.
//
// gomail.Send always wraps the underlying failure as *gomail.SendError, which
// has no Unwrap method, so errors.As/errors.Is would never see through it to
// the real net.Error/io.EOF/syscall cause. Unwrap that one layer by hand
// before classifying.
func transportError(err error) bool {
	var se *gomail.SendError
	cause := err
	if errors.As(err, &se) && se.Cause != nil {
		cause = se.Cause
	}

	var nerr net.Error
	if errors.As(cause, &nerr) && nerr.Timeout() {
		return true
	}
	return errors.Is(cause, io.EOF) || errors.Is(cause, syscall.ECONNRESET) || errors.Is(cause, syscall.EPIPE)
}

func (e *EmailSender) dialer() *gomail.Dialer {
	d := gomail.NewDialer(e.cfg.Host, e.cfg.Port, e.cfg.Username, e.cfg.Password)
	if e.cfg.Timeout > 0 {
		d.Timeout = e.cfg.Timeout
	}
	// AuthType is enforced at config validation (PLAIN/LOGIN only); at runtime
	// gomail negotiates the mechanism from the server's advertised AUTH list.
	// The configured value does not force a mechanism.
	if e.cfg.TLS.Enabled {
		d.TLSConfig = &tls.Config{ServerName: e.cfg.TLS.ServerName}
		if e.cfg.Port == 465 {
			d.SSL = true // implicit TLS
		} else {
			d.StartTLSPolicy = gomail.MandatoryStartTLS
		}
	}
	// When TLS.Enabled is false, gomail's defaults still apply: implicit TLS
	// on port 465, opportunistic STARTTLS elsewhere.
	return d
}

func (e *EmailSender) Send(ctx context.Context, n *models.Notification) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	p := n.Email
	if p == nil {
		return fmt.Errorf("notification has no email payload")
	}

	m := gomail.NewMessage()
	// The policy From identity is atomic: when the payload carries an
	// address, its (possibly empty) display name is used as-is — the
	// provider's FromName is not mixed in.
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
		// The connection is suspect either way: drop it so the next call
		// starts clean.
		_ = e.closer.Close()
		e.closer = nil

		if !transportError(err) {
			// Protocol-level SMTP rejection (bad recipient, policy reject,
			// etc.) would fail identically on retry, and retrying risks
			// double-delivering a message whose DATA was already accepted.
			return fmt.Errorf("smtp send: %w", err)
		}

		// Stale connection (server idle-closed, network blip): re-dial once.
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
