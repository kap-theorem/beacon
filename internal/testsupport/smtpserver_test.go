package testsupport

import (
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
	if !contains(msgs[0].Data, "Subject: hello") {
		t.Errorf("subject not found in data: %q", msgs[0].Data)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
