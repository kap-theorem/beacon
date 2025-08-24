package email

import (
	"context"
	"fmt"
	"log"

	"beacon/internal/notification"
)

type Config struct {
	SMTPHost     string
	SMTPPort     int
	Username     string
	Password     string
	FromAddress  string
	FromName     string
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
	return notification.Email
}

func (p *Provider) Send(ctx context.Context, msg *notification.Message) error {
	if msg.Type != notification.Email {
		return fmt.Errorf("invalid message type for email provider: %s", msg.Type)
	}

	log.Printf("Sending email to %s - Subject: %s, Body: %s", 
		msg.Recipient, msg.Subject, msg.Body)
	
	return nil
}