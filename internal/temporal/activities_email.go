package temporal

import (
	"beacon/internal/notifier"
	"context"

	"beacon/internal/models"

	"github.com/google/uuid"
)

type EmailActivities struct {
	GetService func() notifier.Notifier[models.EmailMessage]
}

func (a *EmailActivities) SendEmailActivity(ctx context.Context, msg *models.EmailMessage) error {
	return a.GetService().Send(ctx, &notifier.Message[models.EmailMessage]{
		ID:   uuid.NewString(),
		Type: notifier.EmailNotifier,
		Data: *msg,
	})
}
