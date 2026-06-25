package notifier

import (
	"beacon/internal/config"
	"sort"
	"strings"
	"testing"
)

// bundle is a convenience constructor that builds a *config.ConfigBundle from
// one or more *config.SMTPClientConfig values, keyed by Name.
func bundle(cfgs ...*config.SMTPClientConfig) *config.ConfigBundle {
	m := map[string]*config.SMTPClientConfig{}
	for _, c := range cfgs {
		m[c.Name] = c
	}
	return &config.ConfigBundle{SMTP: m, Revision: 1}
}

// ---------------------------------------------------------------------------
// NewEmailClientRegistry
// ---------------------------------------------------------------------------

func TestNewEmailClientRegistry_NilBundle(t *testing.T) {
	_, err := NewEmailClientRegistry(nil)
	if err == nil {
		t.Fatal("expected error for nil bundle, got nil")
	}
}

func TestNewEmailClientRegistry_EmptyBundle(t *testing.T) {
	_, err := NewEmailClientRegistry(&config.ConfigBundle{SMTP: map[string]*config.SMTPClientConfig{}})
	if err == nil {
		t.Fatal("expected error for empty bundle, got nil")
	}
}

func TestNewEmailClientRegistry_SingleProvider_AutoDefault(t *testing.T) {
	b := bundle(&config.SMTPClientConfig{Name: "solo", Host: "localhost", Port: 25})
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.defaultKey != "solo" {
		t.Errorf("expected auto-default \"solo\", got %q", r.defaultKey)
	}
}

func TestNewEmailClientRegistry_ExplicitDefault(t *testing.T) {
	b := bundle(
		&config.SMTPClientConfig{Name: "primary", Host: "smtp1", Port: 25, IsDefault: true},
		&config.SMTPClientConfig{Name: "secondary", Host: "smtp2", Port: 25},
	)
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.defaultKey != "primary" {
		t.Errorf("expected default \"primary\", got %q", r.defaultKey)
	}
}

func TestNewEmailClientRegistry_MultipleProviders_NoDefault(t *testing.T) {
	// Two providers, neither is_default — no auto-default should be set.
	b := bundle(
		&config.SMTPClientConfig{Name: "a", Host: "h1", Port: 25},
		&config.SMTPClientConfig{Name: "b", Host: "h2", Port: 25},
	)
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.defaultKey != "" {
		t.Errorf("expected no default, got %q", r.defaultKey)
	}
}

func TestNewEmailClientRegistry_CategoryMapping(t *testing.T) {
	b := bundle(&config.SMTPClientConfig{
		Name:       "p1",
		Host:       "localhost",
		Port:       25,
		Categories: []string{"transactional", "alerts"},
		IsDefault:  true,
	})
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prov, ok := r.categories["transactional"]; !ok || prov != "p1" {
		t.Errorf("category transactional: got %q ok=%v", prov, ok)
	}
	if prov, ok := r.categories["alerts"]; !ok || prov != "p1" {
		t.Errorf("category alerts: got %q ok=%v", prov, ok)
	}
}

// ---------------------------------------------------------------------------
// Resolve
// ---------------------------------------------------------------------------

func TestResolve_KnownHintMatchesCategory(t *testing.T) {
	b := bundle(&config.SMTPClientConfig{
		Name:       "tx-provider",
		Host:       "localhost",
		Port:       25,
		Categories: []string{"transactional"},
		IsDefault:  true,
	})
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatal(err)
	}
	svc, name, err := r.Resolve("transactional")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if name != "tx-provider" {
		t.Errorf("expected name %q, got %q", "tx-provider", name)
	}
	if svc == nil {
		t.Error("expected non-nil EmailService")
	}
}

func TestResolve_UnknownHintFallsBackToDefault(t *testing.T) {
	b := bundle(&config.SMTPClientConfig{Name: "p1", Host: "localhost", Port: 25, Categories: []string{"tx"}, IsDefault: true})
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatal(err)
	}
	_, name, err := r.Resolve("nope")
	if err != nil || name != "p1" {
		t.Fatalf("got name=%q err=%v", name, err)
	}
}

func TestResolve_EmptyHintFallsBackToDefault(t *testing.T) {
	b := bundle(&config.SMTPClientConfig{Name: "def", Host: "localhost", Port: 25, IsDefault: true})
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatal(err)
	}
	_, name, err := r.Resolve("")
	if err != nil {
		t.Fatalf("Resolve empty hint: %v", err)
	}
	if name != "def" {
		t.Errorf("expected default %q, got %q", "def", name)
	}
}

func TestResolve_NoDefaultAndUnknownHint_Error(t *testing.T) {
	b := bundle(
		&config.SMTPClientConfig{Name: "a", Host: "h1", Port: 25},
		&config.SMTPClientConfig{Name: "b", Host: "h2", Port: 25},
	)
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = r.Resolve("unknown")
	if err == nil {
		t.Fatal("expected error for unknown hint with no default, got nil")
	}
}

func TestResolve_NoDefaultAndEmptyHint_Error(t *testing.T) {
	b := bundle(
		&config.SMTPClientConfig{Name: "a", Host: "h1", Port: 25},
		&config.SMTPClientConfig{Name: "b", Host: "h2", Port: 25},
	)
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = r.Resolve("")
	if err == nil {
		t.Fatal("expected error for empty hint with no default, got nil")
	}
}

func TestResolve_AutoDefaultSingleProvider(t *testing.T) {
	b := bundle(&config.SMTPClientConfig{Name: "only", Host: "localhost", Port: 25})
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatal(err)
	}
	_, name, err := r.Resolve("")
	if err != nil {
		t.Fatalf("Resolve with auto-default: %v", err)
	}
	if name != "only" {
		t.Errorf("expected %q, got %q", "only", name)
	}
}

// ---------------------------------------------------------------------------
// Reload
// ---------------------------------------------------------------------------

func TestReload_ReplacesContents(t *testing.T) {
	b1 := bundle(&config.SMTPClientConfig{Name: "old", Host: "h1", Port: 25, IsDefault: true})
	r, err := NewEmailClientRegistry(b1)
	if err != nil {
		t.Fatal(err)
	}

	b2 := bundle(&config.SMTPClientConfig{Name: "new", Host: "h2", Port: 587, IsDefault: true})
	if err := r.Reload(b2); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	names := r.ProviderNames()
	if len(names) != 1 || names[0] != "new" {
		t.Errorf("after Reload expected [\"new\"], got %v", names)
	}
	if r.defaultKey != "new" {
		t.Errorf("after Reload default should be \"new\", got %q", r.defaultKey)
	}
}

func TestReload_ReplacesCategories(t *testing.T) {
	b1 := bundle(&config.SMTPClientConfig{
		Name:       "old",
		Host:       "h1",
		Port:       25,
		Categories: []string{"cat-old"},
		IsDefault:  true,
	})
	r, err := NewEmailClientRegistry(b1)
	if err != nil {
		t.Fatal(err)
	}

	b2 := bundle(&config.SMTPClientConfig{
		Name:       "new",
		Host:       "h2",
		Port:       25,
		Categories: []string{"cat-new"},
		IsDefault:  true,
	})
	if err := r.Reload(b2); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if _, ok := r.categories["cat-old"]; ok {
		t.Error("old category should have been removed after Reload")
	}
	if prov, ok := r.categories["cat-new"]; !ok || prov != "new" {
		t.Errorf("new category not mapped correctly: prov=%q ok=%v", prov, ok)
	}
}

func TestReload_EmptyBundle_Error(t *testing.T) {
	b := bundle(&config.SMTPClientConfig{Name: "p1", Host: "h1", Port: 25, IsDefault: true})
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Reload(nil); err == nil {
		t.Fatal("expected error reloading with nil bundle, got nil")
	}
	if err := r.Reload(&config.ConfigBundle{SMTP: map[string]*config.SMTPClientConfig{}}); err == nil {
		t.Fatal("expected error reloading with empty SMTP map, got nil")
	}
}

func TestReload_AutoDefaultSingleProvider(t *testing.T) {
	b1 := bundle(&config.SMTPClientConfig{Name: "first", Host: "h1", Port: 25, IsDefault: true})
	r, err := NewEmailClientRegistry(b1)
	if err != nil {
		t.Fatal(err)
	}

	// Reload with a single provider that has no IsDefault — should be auto-defaulted.
	b2 := bundle(&config.SMTPClientConfig{Name: "second", Host: "h2", Port: 25})
	if err := r.Reload(b2); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if r.defaultKey != "second" {
		t.Errorf("expected auto-default \"second\" after Reload, got %q", r.defaultKey)
	}
}

// ---------------------------------------------------------------------------
// TaskQueueFor
// ---------------------------------------------------------------------------

func TestTaskQueueFor(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"sendgrid", "email-sendgrid-queue"},
		{"mailgun", "email-mailgun-queue"},
		{"", "email--queue"},
	}
	for _, tt := range tests {
		got := TaskQueueFor(tt.provider)
		if got != tt.want {
			t.Errorf("TaskQueueFor(%q) = %q, want %q", tt.provider, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// ProviderNames
// ---------------------------------------------------------------------------

func TestProviderNames_Sorted(t *testing.T) {
	b := bundle(
		&config.SMTPClientConfig{Name: "zebra", Host: "h1", Port: 25},
		&config.SMTPClientConfig{Name: "alpha", Host: "h2", Port: 25},
		&config.SMTPClientConfig{Name: "middle", Host: "h3", Port: 25},
	)
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatal(err)
	}

	names := r.ProviderNames()
	if !sort.StringsAreSorted(names) {
		t.Errorf("ProviderNames not sorted: %v", names)
	}
	if len(names) != 3 {
		t.Errorf("expected 3 names, got %d: %v", len(names), names)
	}
}

func TestProviderNames_SingleEntry(t *testing.T) {
	b := bundle(&config.SMTPClientConfig{Name: "only", Host: "localhost", Port: 25})
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatal(err)
	}
	names := r.ProviderNames()
	if len(names) != 1 || names[0] != "only" {
		t.Errorf("expected [\"only\"], got %v", names)
	}
}

// ---------------------------------------------------------------------------
// Resolve edge cases: category mapped to unknown provider
// ---------------------------------------------------------------------------

// TestResolve_CategoryMapsToUnknownProvider exercises the defensive branch
// in Resolve where a category key exists but the referenced provider has been
// removed from r.clients.
func TestResolve_CategoryMapsToUnknownProvider(t *testing.T) {
	b := bundle(&config.SMTPClientConfig{
		Name:       "real",
		Host:       "localhost",
		Port:       25,
		Categories: []string{"cat"},
		IsDefault:  true,
	})
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatal(err)
	}

	// Manually corrupt state to trigger the defensive path.
	r.mu.Lock()
	r.categories["cat"] = "ghost" // category points to a provider that doesn't exist
	r.mu.Unlock()

	_, _, err = r.Resolve("cat")
	if err == nil {
		t.Fatal("expected error when category maps to unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should mention the missing provider name, got: %v", err)
	}
}

// TestResolve_DefaultKeyMissingFromClients exercises the defensive branch
// in Resolve where the default key exists but has no corresponding client.
func TestResolve_DefaultKeyMissingFromClients(t *testing.T) {
	b := bundle(&config.SMTPClientConfig{Name: "p1", Host: "localhost", Port: 25, IsDefault: true})
	r, err := NewEmailClientRegistry(b)
	if err != nil {
		t.Fatal(err)
	}

	// Manually corrupt state: clear clients but keep the defaultKey pointer.
	r.mu.Lock()
	r.clients = make(map[string]*EmailService)
	r.mu.Unlock()

	_, _, err = r.Resolve("")
	if err == nil {
		t.Fatal("expected error when default key has no registered client, got nil")
	}
}
