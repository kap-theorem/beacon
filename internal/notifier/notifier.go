package notifier

import "context"

// NotifierType represents the type of notification channel.
type NotifierType string

// Message represents a notification message with generic data type.
type Message[T any] struct {
	ID       string
	Type     NotifierType
	Data     T
	Metadata map[string]string
}

// Notifier defines the interface for sending notifications of
// generic data type.
type Notifier[T any] interface {
	Send(ctx context.Context, msg *Message[T]) error
}
