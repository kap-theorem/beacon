// Package policy enforces per-service channel policy: provider binding and
// rate limits. Sender-identity injection uses config.ChannelPolicy.From
// directly in the API handler.
package policy

import (
	"fmt"
	"slices"

	"beacon/internal/config"
)

// ResolveProvider applies provider binding: empty request -> policy default;
// otherwise the requested provider must be in the service's allowlist.
func ResolveProvider(pol *config.ChannelPolicy, requested string) (string, error) {
	if requested == "" {
		return pol.DefaultProvider, nil
	}
	if !slices.Contains(pol.Providers, requested) {
		return "", fmt.Errorf("provider %q not allowed for this service", requested)
	}
	return requested, nil
}
