// Package testsupport provides shared test helpers (in-process mock SMTP server).
package testsupport

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// CapturedMessage is a single message accepted by the mock server.
type CapturedMessage struct {
	From string
	To   []string
	Data string
}

// MockSMTPServer is a minimal in-process SMTP server for tests. It accepts the
// subset of SMTP that gopkg.in/mail.v2 uses with no auth and no STARTTLS.
type MockSMTPServer struct {
	ln          net.Listener
	mu          sync.Mutex
	messages    []CapturedMessage
	connections atomic.Int64
}

// NewMockSMTPServer starts the server on a random localhost port and registers
// cleanup with t.
func NewMockSMTPServer(t *testing.T) *MockSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &MockSMTPServer{ln: ln}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *MockSMTPServer) Host() string { return "127.0.0.1" }

func (s *MockSMTPServer) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Connections returns the number of TCP connections accepted so far.
func (s *MockSMTPServer) Connections() int { return int(s.connections.Load()) }

// Messages returns a copy of all captured messages.
func (s *MockSMTPServer) Messages() []CapturedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CapturedMessage, len(s.messages))
	copy(out, s.messages)
	return out
}

func (s *MockSMTPServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		s.connections.Add(1)
		go s.handle(conn)
	}
}

func (s *MockSMTPServer) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	write := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}

	write("220 mock.local ESMTP")
	var msg CapturedMessage
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			// Advertise nothing extra: no STARTTLS, no AUTH.
			write("250-mock.local")
			write("250 OK")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			msg.From = extractAddr(line)
			write("250 OK")
		case strings.HasPrefix(cmd, "RCPT TO"):
			msg.To = append(msg.To, extractAddr(line))
			write("250 OK")
		case cmd == "DATA":
			write("354 End data with <CR><LF>.<CR><LF>")
			var sb strings.Builder
			for {
				dl, derr := r.ReadString('\n')
				if derr != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				sb.WriteString(dl)
			}
			msg.Data = sb.String()
			s.mu.Lock()
			s.messages = append(s.messages, msg)
			s.mu.Unlock()
			msg = CapturedMessage{}
			write("250 OK: queued")
		case cmd == "RSET":
			msg = CapturedMessage{}
			write("250 OK")
		case cmd == "NOOP":
			write("250 OK")
		case cmd == "QUIT":
			write("221 Bye")
			return
		default:
			write("250 OK")
		}
	}
}

// extractAddr pulls the address out of "MAIL FROM:<addr>" / "RCPT TO:<addr>".
func extractAddr(line string) string {
	start := strings.Index(line, "<")
	end := strings.Index(line, ">")
	if start >= 0 && end > start {
		return line[start+1 : end]
	}
	return strings.TrimSpace(line)
}
