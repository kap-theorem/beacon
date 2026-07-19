// Package channel defines the seam new notification channels plug into.
// Auth, policy, rate limiting, idempotency, and the DLQ operate on the
// Notification envelope and never inspect channel payloads.
package channel

import "beacon/internal/models"

// Request is a decoded, semantically valid notify request for one channel.
type Request struct {
	Provider     string // optional provider requested by the caller ("" = policy default)
	Notification *models.Notification
}

// Channel is one notification channel implementation.
type Channel interface {
	Name() string
	DecodeRequest(body []byte) (*Request, error)
	TaskQueue(provider string) string
	WorkflowName() string
}

// Registry maps channel name -> implementation.
// Registries are built once at startup and read concurrently; treat as read-only after NewRegistry.
type Registry map[string]Channel

// NewRegistry registers all built-in channels.
func NewRegistry() Registry {
	email := NewEmailChannel()
	return Registry{email.Name(): email}
}

// TaskQueue is the canonical task-queue naming scheme.
func TaskQueue(channelName, provider string) string {
	return channelName + "-" + provider + "-queue"
}
