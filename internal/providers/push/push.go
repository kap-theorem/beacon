package push

import (
	"context"
	"fmt"
	"log"

	"beacon/internal/notification"
)

type Config struct {
	FCMServerKey string
	APNSKeyID    string
	APNSTeamID   string
	APNSKey      []byte
}

type Provider struct {
	config *Config
}

func NewProvider(config *Config) *Provider {
	return &Provider{
		config: config,
	}
}

func (p *Provider) GetType() notification.NotificationType {
	return notification.Push
}

func (p *Provider) Send(ctx context.Context, msg *notification.Message) error {
	if msg.Type != notification.Push {
		return fmt.Errorf("invalid message type for push provider: %s", msg.Type)
	}

	deviceToken, ok := msg.Metadata["device_token"].(string)
	if !ok {
		return fmt.Errorf("device_token is required in metadata for push notifications")
	}

	log.Printf("Sending push notification to device %s - Subject: %s, Body: %s", 
		deviceToken, msg.Subject, msg.Body)
	
	return nil
}