package testsupport

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	gomail "gopkg.in/mail.v2"
)

func TestMockSMTPServer_CapturesMessage(t *testing.T) {
	srv := NewMockSMTPServer(t) // starts on a random localhost port, registers cleanup

	m := gomail.NewMessage()
	m.SetAddressHeader("From", "beacon@local", "Beacon")
	m.SetHeader("To", "alice@example.com")
	m.SetHeader("Subject", "hello")
	m.SetBody("text/plain", "world")

	d := gomail.NewDialer(srv.Host(), srv.Port(), "", "") // empty creds => no AUTH
	if err := d.DialAndSend(m); err != nil {
		t.Fatalf("DialAndSend: %v", err)
	}

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("want 1 captured message, got %d", len(msgs))
	}
	if msgs[0].To[0] != "alice@example.com" {
		t.Errorf("recipient: got %q", msgs[0].To[0])
	}
	if !strings.Contains(msgs[0].Data, "Subject: hello") {
		t.Errorf("subject not found in data: %q", msgs[0].Data)
	}
}

// TestMockSMTPServer_RawCommands drives the server over a raw TCP connection to
// exercise the command branches gomail does not use (NOOP, RSET, an unknown
// command falling through to the default arm) and extractAddr's fallback when an
// address arrives without angle brackets. It also completes a full MAIL/RCPT/DATA
// cycle so the captured message is asserted.
func TestMockSMTPServer_RawCommands(t *testing.T) {
	srv := NewMockSMTPServer(t)

	conn, err := net.Dial("tcp", net.JoinHostPort(srv.Host(), strconv.Itoa(srv.Port())))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	r := bufio.NewReader(conn)
	readLine := func() string {
		line, rerr := r.ReadString('\n')
		if rerr != nil {
			t.Fatalf("read: %v", rerr)
		}
		return line
	}
	send := func(s string) {
		if _, werr := conn.Write([]byte(s + "\r\n")); werr != nil {
			t.Fatalf("write %q: %v", s, werr)
		}
	}

	readLine() // 220 greeting
	send("EHLO test.local")
	readLine() // 250-mock.local
	readLine() // 250 OK
	send("NOOP")
	if got := readLine(); !strings.HasPrefix(got, "250") {
		t.Errorf("NOOP: got %q", got)
	}
	send("RSET")
	if got := readLine(); !strings.HasPrefix(got, "250") {
		t.Errorf("RSET: got %q", got)
	}
	send("VRFY nobody") // unknown command -> default arm
	if got := readLine(); !strings.HasPrefix(got, "250") {
		t.Errorf("default: got %q", got)
	}
	send("MAIL FROM:bare@local") // no angle brackets -> extractAddr fallback
	readLine()
	send("RCPT TO:<rcpt@example.com>")
	readLine()
	send("DATA")
	readLine() // 354
	send("Subject: raw")
	send("")
	send("body line")
	send(".")
	readLine() // 250 OK: queued
	send("QUIT")
	readLine() // 221 Bye

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("want 1 captured message, got %d", len(msgs))
	}
	if len(msgs[0].To) != 1 || msgs[0].To[0] != "rcpt@example.com" {
		t.Errorf("recipient: got %+v", msgs[0].To)
	}
	if !strings.Contains(msgs[0].Data, "Subject: raw") {
		t.Errorf("subject not found in data: %q", msgs[0].Data)
	}
}

// TestMockSMTPServer_ConnectionsAndCloseActiveConns dials the server directly
// (bypassing gomail entirely) to exercise Connections() and CloseActiveConns()
// within this package's own test binary: the coverage gate is measured
// per-package, and these two methods are otherwise only invoked from
// internal/notifier's tests, which does not count toward this package's own
// coverage report.
func TestMockSMTPServer_ConnectionsAndCloseActiveConns(t *testing.T) {
	srv := NewMockSMTPServer(t)

	conn, err := net.Dial("tcp", net.JoinHostPort(srv.Host(), strconv.Itoa(srv.Port())))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	r := bufio.NewReader(conn)
	greeting, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read greeting: %v", err)
	}
	if !strings.HasPrefix(greeting, "220") {
		t.Fatalf("greeting = %q, want 220 prefix", greeting)
	}

	if got := srv.Connections(); got != 1 {
		t.Fatalf("Connections() = %d, want 1", got)
	}

	srv.CloseActiveConns()

	// The connection must be dead now: a read should fail promptly (EOF or
	// reset) rather than block. Bound it with a deadline so a regression
	// (server not actually dropping the conn) fails the test instead of
	// hanging it.
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, err := r.ReadString('\n'); err == nil {
		t.Fatal("read after CloseActiveConns succeeded, want error (connection should be dead)")
	}
}

// TestMockSMTPServer_Stop verifies Stop() closes the listening socket so no
// further connections can be accepted, and that calling it twice does not
// panic (the mock discards the second, already-closed error).
func TestMockSMTPServer_Stop(t *testing.T) {
	srv := NewMockSMTPServer(t)
	addr := net.JoinHostPort(srv.Host(), strconv.Itoa(srv.Port()))

	// Sanity check: the server accepts connections before Stop.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial before Stop: %v", err)
	}
	conn.Close()

	srv.Stop()
	srv.Stop() // idempotent: must not panic

	if _, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
		t.Fatal("dial after Stop succeeded, want connection refused")
	}
}

// TestMockSMTPServer_RejectNextMail drives the server over a raw connection to
// verify RejectNextMail arms a one-shot protocol-level rejection of the next
// MAIL FROM (rather than the usual "250 OK"), and that the arming is consumed
// so a subsequent MAIL FROM on the same connection proceeds normally.
func TestMockSMTPServer_RejectNextMail(t *testing.T) {
	srv := NewMockSMTPServer(t)
	srv.RejectNextMail(550, "mailbox unavailable")

	conn, err := net.Dial("tcp", net.JoinHostPort(srv.Host(), strconv.Itoa(srv.Port())))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	r := bufio.NewReader(conn)
	readLine := func() string {
		line, rerr := r.ReadString('\n')
		if rerr != nil {
			t.Fatalf("read: %v", rerr)
		}
		return line
	}
	send := func(s string) {
		if _, werr := conn.Write([]byte(s + "\r\n")); werr != nil {
			t.Fatalf("write %q: %v", s, werr)
		}
	}

	readLine() // 220 greeting
	send("EHLO test.local")
	readLine() // 250-mock.local
	readLine() // 250 OK

	send("MAIL FROM:<a@b.com>")
	if got := readLine(); !strings.HasPrefix(got, "550 mailbox unavailable") {
		t.Fatalf("rejected MAIL FROM reply = %q, want 550 mailbox unavailable", got)
	}

	// The arming was consumed: a second MAIL FROM must succeed normally and
	// complete a full send cycle.
	send("MAIL FROM:<a@b.com>")
	if got := readLine(); !strings.HasPrefix(got, "250") {
		t.Fatalf("second MAIL FROM reply = %q, want 250", got)
	}
	send("RCPT TO:<rcpt@example.com>")
	readLine()
	send("DATA")
	readLine() // 354
	send("Subject: after-reject")
	send("")
	send("body")
	send(".")
	if got := readLine(); !strings.HasPrefix(got, "250") {
		t.Fatalf("DATA reply = %q, want 250", got)
	}
	send("QUIT")
	readLine()

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("want 1 captured message (the rejected attempt must not be captured), got %d", len(msgs))
	}
}
