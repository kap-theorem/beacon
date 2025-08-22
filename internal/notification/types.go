package notification

import "context"

type NotificationType string

const (
	SMS   NotificationType = "sms"
	Email NotificationType = "email"
	Push  NotificationType = "push"
)

type Message struct {
	ID        string                 `json:"id"`
	Type      NotificationType       `json:"type"`
	Recipient string                 `json:"recipient"`
	Subject   string                 `json:"subject,omitempty"`
	Body      string                 `json:"body"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type NotificationProvider interface {
	Send(ctx context.Context, msg *Message) error
	GetType() NotificationType
}

type NotificationService interface {
	SendNotification(ctx context.Context, msg *Message) error
	RegisterProvider(provider NotificationProvider) error
}