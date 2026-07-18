package config

// DefaultProviderName returns the provider name (SMTP map key) to use when no
// explicit provider is requested: the provider marked is_default, or — when
// none is marked and exactly one provider exists — that sole provider.
// Returns "" when no default can be determined.
func DefaultProviderName(bundle *ConfigBundle) string {
	if bundle == nil {
		return ""
	}
	for name, cfg := range bundle.SMTP {
		if cfg.IsDefault {
			return name
		}
	}
	if len(bundle.SMTP) == 1 {
		for name := range bundle.SMTP {
			return name
		}
	}
	return ""
}
