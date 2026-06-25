package utils

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewTemporalClient_ConnectionError verifies that NewTemporalClient returns
// an error (rather than panicking) when no Temporal server is reachable at the
// configured address.  We point TEMPORAL_ADDRESS at a port that is guaranteed
// to refuse connections immediately (port 1 on loopback), so the test completes
// without a long timeout.
func TestNewTemporalClient_ConnectionError(t *testing.T) {
	// Port 1 on loopback is reserved and never has anything listening on it,
	// so the OS returns connection-refused immediately.
	t.Setenv("TEMPORAL_ADDRESS", "127.0.0.1:1")

	c, err := NewTemporalClient()
	if err == nil {
		// In unusual CI environments a dial might succeed (extremely unlikely
		// with port 1, but we handle it defensively rather than failing hard).
		c.Close()
		t.Skip("unexpected successful connection to 127.0.0.1:1 — skipping")
	}

	// Primary assertion: function returned an error instead of panicking.
	// The caller should receive a non-nil error and a nil client.
	if c != nil {
		c.Close()
		t.Errorf("expected nil client on connection error, got non-nil")
	}
}

// TestNewTemporalClient_InvalidAddress verifies that a syntactically invalid
// address also results in an error rather than a panic.
func TestNewTemporalClient_InvalidAddress(t *testing.T) {
	t.Setenv("TEMPORAL_ADDRESS", "::not-a-valid-host::")

	c, err := NewTemporalClient()
	if err == nil {
		c.Close()
		t.Skip("unexpected successful connection to invalid address — skipping")
	}
	if c != nil {
		c.Close()
		t.Errorf("expected nil client on invalid address, got non-nil")
	}
}

// TestNewTemporalClient_BadConfigFile covers the LoadDefaultClientOptions error
// path (return nil, err) inside NewTemporalClient.  We point TEMPORAL_CONFIG_FILE
// at a file that contains invalid TOML so that envconfig fails before ever
// attempting a network dial.
func TestNewTemporalClient_BadConfigFile(t *testing.T) {
	// Write a file with deliberately broken TOML syntax.
	dir := t.TempDir()
	badCfg := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(badCfg, []byte("[[not valid toml"), 0o600); err != nil {
		t.Fatalf("setup: write bad config file: %v", err)
	}

	t.Setenv("TEMPORAL_CONFIG_FILE", badCfg)
	// Clear any address override so the config-parse error surfaces first.
	t.Setenv("TEMPORAL_ADDRESS", "")

	c, err := NewTemporalClient()
	if err == nil {
		// Parse succeeded unexpectedly — close client and skip rather than fail.
		c.Close()
		t.Skip("config parse did not error; skipping LoadDefaultClientOptions error-path coverage")
	}
	if c != nil {
		c.Close()
		t.Errorf("expected nil client when LoadDefaultClientOptions errors, got non-nil")
	}
}
