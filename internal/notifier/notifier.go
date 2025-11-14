package notifier

import "context"

// NotifierType represents the type of notification channel.
type NotifierType string

const (
	SMSNotifier   NotifierType = "sms"
	EmailNotifier NotifierType = "email"
)

type Message[T any] struct {
	ID       string
	Type     NotifierType
	Data     T
	Metadata map[string]string
}

type Notifier[T any] interface {
	Send(ctx context.Context, msg *Message[T]) error
}
