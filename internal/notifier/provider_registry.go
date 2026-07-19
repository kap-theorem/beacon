package notifier

import (
	"slices"
	"sync"

	"beacon/internal/config"
)

// ProviderRegistry tracks which providers exist per channel. The server uses
// it for existence checks and routing; workers construct concrete senders
// from the config service directly.
type ProviderRegistry struct {
	mu        sync.RWMutex
	byChannel map[string]map[string]bool
}

func NewProviderRegistry(bundle *config.ConfigBundle) *ProviderRegistry {
	r := &ProviderRegistry{}
	r.Reload(bundle)
	return r
}

// Reload atomically replaces the provider set from a config bundle.
func (r *ProviderRegistry) Reload(bundle *config.ConfigBundle) {
	byChannel := map[string]map[string]bool{"email": {}}
	if bundle != nil {
		for name := range bundle.SMTP {
			byChannel["email"][name] = true
		}
	}
	r.mu.Lock()
	r.byChannel = byChannel
	r.mu.Unlock()
}

func (r *ProviderRegistry) Exists(channel, provider string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byChannel[channel][provider]
}

// Names returns sorted provider names for a channel.
func (r *ProviderRegistry) Names(channel string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.byChannel[channel]))
	for name := range r.byChannel[channel] {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
