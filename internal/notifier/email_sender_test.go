package notifier

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
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

// TestTransportError_NonTimeoutNetError pins the remaining branch: a net.Error
// whose Timeout() is false (so the timeout fast-path is skipped) and whose
// cause is none of io.EOF/ECONNRESET/EPIPE must still classify as NOT a
// transport error, rather than falling through to true by accident.
func TestTransportError_NonTimeoutNetError(t *testing.T) {
	nonTimeout := &net.DNSError{Err: "no such host", IsTimeout: false}
	if transportError(nonTimeout) {
		t.Fatal("a non-timeout net.Error with no EOF/ECONNRESET/EPIPE cause must NOT classify as a transport error")
	}
	if transportError(&gomail.SendError{Cause: nonTimeout}) {
		t.Fatal("a non-timeout net.Error wrapped in SendError must NOT classify as a transport error")
	}
}

// TestEmailSender_DialFailure exercises the dial-failure return path in Send:
// the very first Send call must dial before it can send anything, so pointing
// the sender at a port nothing is listening on must surface a "smtp dial:"
// error rather than panicking or returning a generic error.
func TestEmailSender_DialFailure(t *testing.T) {
	// Grab a free localhost port and immediately close the listener so
	// nothing is listening on it, guaranteeing a fast "connection refused"
	// dial failure instead of a slow timeout against an unroutable address.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	sender := NewEmailSender(&config.SMTPClientConfig{
		Host: "127.0.0.1", Port: port, Timeout: 1 * time.Second,
	})
	defer sender.Close()

	n := &models.Notification{
		Email: &models.EmailPayload{To: "a@b.com", Subject: "s", Body: "b"},
	}
	err = sender.Send(context.Background(), n)
	if err == nil {
		t.Fatal("expected dial error, got nil")
	}
	if !strings.Contains(err.Error(), "smtp dial:") {
		t.Fatalf("error = %v, want containing %q", err, "smtp dial:")
	}
}

// TestEmailSender_RedialFailureAfterSendFailure exercises the re-dial-fails
// return path: a live connection goes bad server-side (forcing Send's
// transport-error branch to attempt one re-dial), but the server has also
// stopped accepting new connections entirely by then, so the re-dial itself
// must fail and Send must surface the combined "smtp re-dial after send
// failure" error rather than silently losing the original cause.
func TestEmailSender_RedialFailureAfterSendFailure(t *testing.T) {
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

	// Drop the live connection server-side (the next Send attempt to reuse it
	// will fail with a transport error and try to re-dial), then stop the
	// listener entirely so that re-dial has nothing to connect to.
	srv.CloseActiveConns()
	srv.Stop()

	n.Email.Subject = "s2"
	err := sender.Send(context.Background(), n)
	if err == nil {
		t.Fatal("expected re-dial failure error, got nil")
	}
	if !strings.Contains(err.Error(), "smtp re-dial after send failure") {
		t.Fatalf("error = %v, want containing %q", err, "smtp re-dial after send failure")
	}
}

// TestEmailSender_SecondSendFailsAfterSuccessfulRedial exercises the final,
// hardest-to-reach failure path in Send: the held connection goes bad
// (transport error, e.g. a server-side drop), the automatic re-dial itself
// succeeds, but the retried send on that fresh connection also fails. Both
// outcomes are fatal (no further retry), and the returned error must wrap the
// *second* failure, not be mistaken for a re-dial failure.
func TestEmailSender_SecondSendFailsAfterSuccessfulRedial(t *testing.T) {
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

	// Kill the held connection server-side (forces the transport-error
	// re-dial branch), and arm a protocol-level rejection for whichever MAIL
	// FROM comes next -- that lands on the fresh, re-dialed connection.
	srv.CloseActiveConns()
	srv.RejectNextMail(550, "mailbox unavailable")

	n.Email.Subject = "s2"
	err := sender.Send(context.Background(), n)
	if err == nil {
		t.Fatal("expected the retried send (on the re-dialed connection) to fail, got nil")
	}
	if !strings.Contains(err.Error(), "smtp send:") {
		t.Fatalf("error = %v, want containing %q", err, "smtp send:")
	}
	if strings.Contains(err.Error(), "re-dial") {
		t.Fatalf("error = %v, must not be reported as a re-dial failure (the re-dial itself succeeded)", err)
	}
	if got := srv.Connections(); got != 2 {
		t.Fatalf("mock server accepted %d connections, want 2 (original + the successful re-dial)", got)
	}
}

// TestEmailSender_NonTransportSendFailureNotRetried exercises the no-retry
// return path: a protocol-level SMTP rejection (mailbox unavailable) on an
// otherwise-healthy connection must be classified as non-transport, surfaced
// immediately as "smtp send:", and must NOT trigger a re-dial/retry attempt
// (retrying risks double-delivering a message whose DATA step already ran).
// A third, unarmed Send is then used to confirm the sender recovers cleanly
// afterward by dialing a fresh connection.
func TestEmailSender_NonTransportSendFailureNotRetried(t *testing.T) {
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

	srv.RejectNextMail(550, "mailbox unavailable")
	n.Email.Subject = "s2"
	err := sender.Send(context.Background(), n)
	if err == nil {
		t.Fatal("expected protocol-rejection error, got nil")
	}
	if !strings.Contains(err.Error(), "smtp send:") {
		t.Fatalf("error = %v, want containing %q", err, "smtp send:")
	}
	if strings.Contains(err.Error(), "re-dial") {
		t.Fatalf("error = %v, must NOT attempt a re-dial for a protocol-level rejection", err)
	}
	if got := len(srv.Messages()); got != 1 {
		t.Fatalf("mock server captured %d messages, want 1 (rejected send must not be captured)", got)
	}

	// The sender must have dropped the bad connection and be ready to dial
	// fresh on the next call.
	n.Email.Subject = "s3"
	if err := sender.Send(context.Background(), n); err != nil {
		t.Fatalf("third send (after protocol rejection): %v", err)
	}
	if got := len(srv.Messages()); got != 2 {
		t.Fatalf("mock server captured %d messages, want 2", got)
	}
	if got := srv.Connections(); got != 2 {
		t.Fatalf("mock server accepted %d connections, want 2 (fresh dial after the rejected send)", got)
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
