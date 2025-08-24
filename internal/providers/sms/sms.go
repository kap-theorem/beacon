package sms

import (
	"context"
	"fmt"
	"log"

	"beacon/internal/notification"
)

type Config struct {
	APIKey     string
	APISecret  string
	BaseURL    string
	FromNumber string
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
	return notification.SMS
}

func (p *Provider) Send(ctx context.Context, msg *notification.Message) error {
	if msg.Type != notification.SMS {
		return fmt.Errorf("invalid message type for SMS provider: %s", msg.Type)
	}

	log.Printf("Sending SMS to %s: %s", msg.Recipient, msg.Body)
	
	return nil
}