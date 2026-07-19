package notifier

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"beacon/internal/config"
	"beacon/internal/models"
	"beacon/internal/testsupport"

	gomail "gopkg.in/mail.v2"
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
	if got := srv.Connections(); got != 1 {
		t.Fatalf("mock server accepted %d connections, want 1 (connection must be reused)", got)
	}
}

// TestEmailSender_RedialsAfterConnectionDrop simulates a broken connection
// (server idle-closed, network blip) by forcing the sender to drop its held
// connection between sends, then asserts the next Send transparently re-dials
// and still succeeds.
func TestEmailSender_RedialsAfterConnectionDrop(t *testing.T) {
	srv := testsupport.NewMockSMTPServer(t)
	sender := NewEmailSender(&config.SMTPClientConfig{
		Host: srv.Host(), Port: srv.Port(),
		Username: "u", Password: "p", AuthType: config.AuthPlain,
		FromAddress: "noreply@corp.com", FromName: "Beacon",
	})
	defer sender.Close()

	n := &models.Notification{
		Email: &models.EmailPayload{To: "a@b.com", Subject: "s1", Body: "b1"},
	}
	if err := sender.Send(context.Background(), n); err != nil {
		t.Fatalf("first send: %v", err)
	}

	// Simulate a dropped/stale connection: close it out from under the
	// sender so the next Send must dial a fresh one.
	sender.Close()

	n.Email.Subject = "s2"
	if err := sender.Send(context.Background(), n); err != nil {
		t.Fatalf("second send (after connection drop): %v", err)
	}
	if got := len(srv.Messages()); got != 2 {
		t.Fatalf("mock server captured %d messages, want 2", got)
	}
	if got := srv.Connections(); got != 2 {
		t.Fatalf("mock server accepted %d connections, want 2 (one dial per connection lifetime)", got)
	}
}

// TestEmailSender_RecoversFromServerSideDrop leaves a live connection open
// (first send succeeds), then has the server forcibly drop every active
// connection out from under the sender — simulating an idle-timeout or
// restart mid-session. The sender does not find out until it tries to reuse
// the connection: gomail.Send must fail on the now-dead socket, the
// unwrapped classifier must recognize that failure as transport-level, and
// the sender must re-dial once and successfully deliver the second message.
func TestEmailSender_RecoversFromServerSideDrop(t *testing.T) {
	srv := testsupport.NewMockSMTPServer(t)
	sender := NewEmailSender(&config.SMTPClientConfig{
		Host: srv.Host(), Port: srv.Port(),
		Username: "u", Password: "p", AuthType: config.AuthPlain,
		FromAddress: "noreply@corp.com", FromName: "Beacon",
	})
	defer sender.Close()

	n := &models.Notification{
		Email: &models.EmailPayload{To: "a@b.com", Subject: "s1", Body: "b1"},
	}
	if err := sender.Send(context.Background(), n); err != nil {
		t.Fatalf("first send: %v", err)
	}

	// Server-side drop: the sender still believes its held connection is
	// live and will attempt to reuse it on the next Send.
	srv.CloseActiveConns()

	n.Email.Subject = "s2"
	if err := sender.Send(context.Background(), n); err != nil {
		t.Fatalf("second send (after server-side drop): %v", err)
	}
	if got := len(srv.Messages()); got != 2 {
		t.Fatalf("mock server captured %d messages, want 2", got)
	}
	if got := srv.Connections(); got != 2 {
		t.Fatalf("mock server accepted %d connections, want 2 (one re-dial after the drop)", got)
	}
}

// TestTransportError_UnwrapsSendError pins the unwrap behavior: gomail.Send
// always wraps the real cause in a *gomail.SendError with no Unwrap method,
// so transportError must reach through that wrapper by hand rather than rely
// on errors.As/errors.Is to see past it on their own.
func TestTransportError_UnwrapsSendError(t *testing.T) {
	if !transportError(&gomail.SendError{Cause: io.EOF}) {
		t.Fatal("io.EOF wrapped in SendError should classify as a transport error")
	}
	if transportError(&gomail.SendError{Cause: errors.New("550 no such user")}) {
		t.Fatal("a protocol-level SMTP rejection wrapped in SendError must NOT classify as a transport error")
	}
	if !transportError(io.EOF) {
		t.Fatal("a bare io.EOF (unwrapped) should still classify as a transport error")
	}
}

func TestEmailSender_DialerTLSMapping(t *testing.T) {
	tests := []struct {
		name           string
		cfg            config.SMTPClientConfig
		wantSSL        bool
		wantStartTLS   gomail.StartTLSPolicy
		wantTLSConfig  bool
		wantServerName string
		wantTimeout    time.Duration
	}{
		{
			name:           "TLS enabled, port 465 -> implicit TLS",
			cfg:            config.SMTPClientConfig{Host: "smtp.example.com", Port: 465, TLS: config.TLSConfig{Enabled: true, ServerName: "smtp.example.com"}},
			wantSSL:        true,
			wantTLSConfig:  true,
			wantServerName: "smtp.example.com",
		},
		{
			name:           "TLS enabled, port 587 -> mandatory STARTTLS",
			cfg:            config.SMTPClientConfig{Host: "smtp.example.com", Port: 587, TLS: config.TLSConfig{Enabled: true, ServerName: "smtp.example.com"}},
			wantSSL:        false,
			wantStartTLS:   gomail.MandatoryStartTLS,
			wantTLSConfig:  true,
			wantServerName: "smtp.example.com",
		},
		{
			name:        "timeout propagated",
			cfg:         config.SMTPClientConfig{Host: "smtp.example.com", Port: 587, Timeout: 7 * time.Second},
			wantTimeout: 7 * time.Second,
		},
		{
			name:          "TLS disabled -> no TLSConfig set",
			cfg:           config.SMTPClientConfig{Host: "smtp.example.com", Port: 587, TLS: config.TLSConfig{Enabled: false}},
			wantTLSConfig: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			sender := NewEmailSender(&cfg)
			d := sender.dialer()

			if d.SSL != tt.wantSSL {
				t.Errorf("d.SSL = %v, want %v", d.SSL, tt.wantSSL)
			}
			if tt.wantStartTLS != 0 && d.StartTLSPolicy != tt.wantStartTLS {
				t.Errorf("d.StartTLSPolicy = %v, want %v", d.StartTLSPolicy, tt.wantStartTLS)
			}
			if tt.wantTLSConfig {
				if d.TLSConfig == nil {
					t.Fatal("d.TLSConfig = nil, want non-nil")
				}
				if d.TLSConfig.ServerName != tt.wantServerName {
					t.Errorf("d.TLSConfig.ServerName = %q, want %q", d.TLSConfig.ServerName, tt.wantServerName)
				}
			} else if d.TLSConfig != nil {
				t.Errorf("d.TLSConfig = %+v, want nil", d.TLSConfig)
			}
			if tt.wantTimeout != 0 && d.Timeout != tt.wantTimeout {
				t.Errorf("d.Timeout = %v, want %v", d.Timeout, tt.wantTimeout)
			}
		})
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
