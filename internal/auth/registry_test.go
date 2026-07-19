package auth

import (
	"testing"

	"beacon/internal/config"
)

func bundleWithService(enabled bool) *config.ConfigBundle {
	key := "bk_k1_secret123"
	return &config.ConfigBundle{
		Services: map[string]*config.ServiceConfig{
			"billing-api": {
				Service: "billing-api", Tenant: "payments", Enabled: enabled,
				Keys: []config.KeyEntry{{ID: "k1", SHA256: HashKey(key), State: "active"}},
				Channels: map[string]*config.ChannelPolicy{
					"email": {Providers: []string{"sendgrid"}, DefaultProvider: "sendgrid",
						Rate: config.RateConfig{RPM: 60, Daily: 5000}},
				},
			},
		},
	}
}

func TestAuthenticate_KnownKey(t *testing.T) {
	r := NewRegistry(bundleWithService(true))
	ident, ok := r.Authenticate("bk_k1_secret123")
	if !ok {
		t.Fatal("expected key to authenticate")
	}
	if ident.Service != "billing-api" || ident.Tenant != "payments" || !ident.Enabled {
		t.Fatalf("unexpected identity: %+v", ident)
	}
	if ident.Channels["email"] == nil {
		t.Fatal("expected channel policy on identity")
	}
}

func TestAuthenticate_UnknownKey(t *testing.T) {
	r := NewRegistry(bundleWithService(true))
	if _, ok := r.Authenticate("bk_k1_wrong"); ok {
		t.Fatal("wrong key must not authenticate")
	}
}

func TestAuthenticate_InactiveKeySkipped(t *testing.T) {
	b := bundleWithService(true)
	b.Services["billing-api"].Keys[0].State = "revoked"
	r := NewRegistry(b)
	if _, ok := r.Authenticate("bk_k1_secret123"); ok {
		t.Fatal("revoked key must not authenticate")
	}
}

func TestReload_SwapsKeys(t *testing.T) {
	r := NewRegistry(bundleWithService(true))
	r.Reload(&config.ConfigBundle{})
	if _, ok := r.Authenticate("bk_k1_secret123"); ok {
		t.Fatal("key must be gone after reload with empty bundle")
	}
}

func TestAuthenticate_RotationOverlap(t *testing.T) {
	b := &config.ConfigBundle{
		Services: map[string]*config.ServiceConfig{
			"billing-api": {
				Service: "billing-api", Tenant: "payments", Enabled: true,
				Keys: []config.KeyEntry{
					{ID: "k1", SHA256: HashKey("bk_k1_secret123"), State: "active"},
					{ID: "k2", SHA256: HashKey("bk_k2_secret456"), State: "active"},
				},
				Channels: map[string]*config.ChannelPolicy{
					"email": {Providers: []string{"sendgrid"}, DefaultProvider: "sendgrid",
						Rate: config.RateConfig{RPM: 60, Daily: 5000}},
				},
			},
		},
	}
	r := NewRegistry(b)

	identK1, ok := r.Authenticate("bk_k1_secret123")
	if !ok {
		t.Fatal("expected k1 to authenticate during rotation overlap")
	}
	identK2, ok := r.Authenticate("bk_k2_secret456")
	if !ok {
		t.Fatal("expected k2 to authenticate during rotation overlap")
	}

	for name, ident := range map[string]*Identity{"k1": identK1, "k2": identK2} {
		if ident.Service != "billing-api" || ident.Tenant != "payments" || !ident.Enabled {
			t.Fatalf("%s: unexpected identity: %+v", name, ident)
		}
	}
}
