// Package auth resolves API keys to service identities.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"beacon/internal/config"
)

// Identity is the authenticated caller attached to the request context.
// Identity and its Channels map are shared across requests and with the
// config bundle; treat as read-only.
type Identity struct {
	Service  string
	Tenant   string
	Enabled  bool
	Admin    bool
	Channels map[string]*config.ChannelPolicy
}

// Registry indexes active API-key hashes to identities. Lookup is by SHA-256
// of the presented key, so plaintext keys are never stored or compared.
type Registry struct {
	mu     sync.RWMutex
	byHash map[string]*Identity
}

func NewRegistry(bundle *config.ConfigBundle) *Registry {
	r := &Registry{}
	r.Reload(bundle)
	return r
}

// Reload atomically replaces the key index from a config bundle.
func (r *Registry) Reload(bundle *config.ConfigBundle) {
	byHash := make(map[string]*Identity)
	if bundle != nil {
		for _, svc := range bundle.Services {
			ident := &Identity{
				Service:  svc.Service,
				Tenant:   svc.Tenant,
				Enabled:  svc.Enabled,
				Channels: svc.Channels,
			}
			for _, k := range svc.Keys {
				if k.State == "active" {
					byHash[strings.ToLower(k.SHA256)] = ident
				}
			}
		}
	}
	r.mu.Lock()
	r.byHash = byHash
	r.mu.Unlock()
}

// HashKey returns the lowercase hex SHA-256 of a full API key.
func HashKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Authenticate resolves a presented key to an identity.
func (r *Registry) Authenticate(token string) (*Identity, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ident, ok := r.byHash[HashKey(token)]
	return ident, ok
}
