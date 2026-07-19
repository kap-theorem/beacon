package notifier

import (
	"context"

	"beacon/internal/models"
)

// Sender delivers one notification over a concrete provider connection.
type Sender interface {
	Send(ctx context.Context, n *models.Notification) error
}
