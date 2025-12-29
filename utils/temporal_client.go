package utils

import (
	"go.temporal.io/sdk/client"
)

// NewTemporalClient creates a new Temporal client connection.
// The caller is responsible for calling Close() on the returned client.
func NewTemporalClient() (client.Client, error) {
	return client.Dial(client.Options{
		HostPort: client.DefaultHostPort,
	})
}
