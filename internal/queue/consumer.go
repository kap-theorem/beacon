package queue

import (
	"context"
	"encoding/json"
	"log"

	"beacon/internal/notification"
)

type MessageHandler func(ctx context.Context, msg *notification.Message) error

type Consumer interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	SetHandler(handler MessageHandler)
}

type MockConsumer struct {
	handler MessageHandler
	stopCh  chan struct{}
}

func NewMockConsumer() *MockConsumer {
	return &MockConsumer{
		stopCh: make(chan struct{}),
	}
}

func (c *MockConsumer) SetHandler(handler MessageHandler) {
	c.handler = handler
}

func (c *MockConsumer) Start(ctx context.Context) error {
	log.Println("Starting mock message queue consumer...")
	
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-c.stopCh:
				return
			default:
				if c.handler != nil {
					mockMessage := &notification.Message{
						ID:        "test-123",
						Type:      notification.Email,
						Recipient: "test@example.com",
						Subject:   "Test Notification",
						Body:      "This is a test message from the queue",
					}
					
					if err := c.handler(ctx, mockMessage); err != nil {
						log.Printf("Error processing message: %v", err)
					}
				}
			}
		}
	}()
	
	return nil
}

func (c *MockConsumer) Stop(ctx context.Context) error {
	log.Println("Stopping mock message queue consumer...")
	close(c.stopCh)
	return nil
}

func ParseMessage(data []byte) (*notification.Message, error) {
	var msg notification.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}