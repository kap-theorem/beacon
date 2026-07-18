package testsupport

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"testing"

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
