package app

import (
	"fmt"
	"strings"

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
	name := config.DefaultProviderName(bundle)
	if name == "" {
		return "", nil, fmt.Errorf("no provider resolved; set PROVIDER_NAME or mark one provider is_default")
	}
	return name, bundle.SMTP[name], nil
}

// ParseWorkerSpec splits a systemd instance name "<channel>-<provider>"
// (e.g. "email-mailgun-payments") into channel and provider. The channel is
// the segment before the first dash; the provider may itself contain dashes.
func ParseWorkerSpec(spec string) (channelName, provider string, err error) {
	before, after, found := strings.Cut(spec, "-")
	if !found || before == "" || after == "" {
		return "", "", fmt.Errorf("worker spec %q must be <channel>-<provider>", spec)
	}
	return before, after, nil
}
