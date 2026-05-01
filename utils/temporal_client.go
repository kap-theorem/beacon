package utils

import (
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
)

// NewTemporalClient creates a new Temporal client connection.
// The caller is responsible for calling Close() on the returned client.
func NewTemporalClient() (client.Client, error) {
	return client.Dial(envconfig.MustLoadDefaultClientOptions())
}
