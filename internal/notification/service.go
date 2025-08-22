package notification

import (
	"context"
	"fmt"
	"log"

	"beacon/internal/retry"
)

type Service struct {
	providers   map[NotificationType]NotificationProvider
	retryConfig *retry.Config
}

func NewService() *Service {
	return &Service{
		providers:   make(map[NotificationType]NotificationProvider),
		retryConfig: retry.DefaultConfig(),
	}
}

func (s *Service) RegisterProvider(provider NotificationProvider) error {
	if provider == nil {
		return fmt.Errorf("provider cannot be nil")
	}
	
	s.providers[provider.GetType()] = provider
	return nil
}

func (s *Service) SendNotification(ctx context.Context, msg *Message) error {
	if msg == nil {
		return fmt.Errorf("message cannot be nil")
	}
	
	provider, exists := s.providers[msg.Type]
	if !exists {
		return fmt.Errorf("no provider registered for notification type: %s", msg.Type)
	}
	
	log.Printf("Sending notification (ID: %s, Type: %s, Recipient: %s)", 
		msg.ID, msg.Type, msg.Recipient)
	
	return retry.WithRetry(ctx, s.retryConfig, func() error {
		return provider.Send(ctx, msg)
	})
}