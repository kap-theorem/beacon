package app

import (
	"fmt"

	"beacon/internal/config"
)

// ResolveWorkerProvider picks the SMTP provider this worker serves. If
// providerName is set it must exist in the bundle. Otherwise the is_default
// provider is used; if none is marked default and exactly one provider exists,
// that one is used. Returns the resolved name and config.
func ResolveWorkerProvider(bundle *config.ConfigBundle, providerName string) (string, *config.SMTPClientConfig, error) {
	if bundle == nil || len(bundle.SMTP) == 0 {
		return "", nil, fmt.Errorf("no SMTP providers in config")
	}
	if providerName != "" {
		cfg, ok := bundle.SMTP[providerName]
		if !ok {
			return "", nil, fmt.Errorf("%w: %s", config.ErrProviderNotFound, providerName)
		}
		return providerName, cfg, nil
	}
	var last *config.SMTPClientConfig
	for _, c := range bundle.SMTP {
		if c.IsDefault {
			return c.Name, c, nil
		}
		last = c
	}
	if len(bundle.SMTP) == 1 {
		return last.Name, last, nil
	}
	return "", nil, fmt.Errorf("no provider resolved; set PROVIDER_NAME or mark one provider is_default")
}
