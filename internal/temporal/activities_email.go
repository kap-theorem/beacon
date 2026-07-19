package temporal

import (
	"context"
	"fmt"

	"beacon/internal/models"
	"beacon/internal/notifier"
)

type EmailActivities struct {
	GetSender func() notifier.Sender
}

func (a *EmailActivities) SendEmailActivity(ctx context.Context, n *models.Notification) error {
	n.Normalize()
	if n.Email == nil {
		return fmt.Errorf("notification has no email payload")
	}
	return a.GetSender().Send(ctx, n)
}
