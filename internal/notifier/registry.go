package notifier

import (
	"beacon/internal/config"
	"fmt"
	"slices"
	"sync"
)

// EmailClientRegistry holds all initialised SMTP clients and resolves routing hints to providers.
type EmailClientRegistry struct {
	mu         sync.RWMutex
	clients    map[string]*EmailService // provider name → service
	categories map[string]string        // category hint → provider name
	defaultKey string
}

// NewEmailClientRegistry builds a registry from a loaded config bundle.
// If exactly one provider exists and none is marked is_default, it becomes the default.
func NewEmailClientRegistry(bundle *config.ConfigBundle) (*EmailClientRegistry, error) {
	r := &EmailClientRegistry{}
	if err := r.Reload(bundle); err != nil {
		return nil, err
	}
	return r, nil
}

// Resolve returns the EmailService and provider name for the given hint.
// Falls back to the default provider when hint is empty or has no matching category.
func (r *EmailClientRegistry) Resolve(hint string) (*EmailService, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if hint != "" {
		if providerName, ok := r.categories[hint]; ok {
			svc, ok := r.clients[providerName]
			if !ok {
				return nil, "", fmt.Errorf("category %q maps to unknown provider %q", hint, providerName)
			}
			return svc, providerName, nil
		}
	}

	if r.defaultKey != "" {
		svc, ok := r.clients[r.defaultKey]
		if !ok {
			return nil, "", fmt.Errorf("default provider %q has no registered client", r.defaultKey)
		}
		return svc, r.defaultKey, nil
	}

	return nil, "", fmt.Errorf("no email client for hint %q and no default provider configured", hint)
}

// Reload atomically replaces the registry contents from a new config bundle.
func (r *EmailClientRegistry) Reload(bundle *config.ConfigBundle) error {
	if bundle == nil || len(bundle.SMTP) == 0 {
		return fmt.Errorf("config bundle has no SMTP providers")
	}

	newClients := make(map[string]*EmailService, len(bundle.SMTP))
	newCategories := make(map[string]string)

	for name, cfg := range bundle.SMTP {
		newClients[name] = NewEmailService(cfg.Host, cfg.Port, cfg.Username, cfg.Password, cfg.FromAddress, cfg.FromName)
		for _, cat := range cfg.Categories {
			newCategories[cat] = name
		}
	}

	newDefault := config.DefaultProviderName(bundle)

	r.mu.Lock()
	r.clients = newClients
	r.categories = newCategories
	r.defaultKey = newDefault
	r.mu.Unlock()

	return nil
}

// TaskQueueFor returns the Temporal task queue name for a provider.
func TaskQueueFor(providerName string) string {
	return "email-" + providerName + "-queue"
}

// ProviderNames returns the registered provider names in sorted order.
func (r *EmailClientRegistry) ProviderNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.clients))
	for name := range r.clients {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
