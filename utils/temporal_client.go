package utils

import (
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/envconfig"
)

// NewTemporalClient creates a new Temporal client connection from environment
// configuration. The caller is responsible for calling Close() on the result.
func NewTemporalClient() (client.Client, error) {
	opts, err := envconfig.LoadDefaultClientOptions()
	if err != nil {
		return nil, err
	}
	return client.Dial(opts)
}
