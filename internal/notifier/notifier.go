package notifier

import "context"

type NotifierType string

const (
	SMSNotifier   NotifierType = "sms"
	EmailNotifier NotifierType = "email"
)

type Message struct {
	ID       string
	Type     NotifierType
	Data     map[string]any
	Metadata map[string]any
}

type Notifier interface {
	Send(ctx context.Context, msg *Message) error
}
