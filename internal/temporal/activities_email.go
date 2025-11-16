package temporal

import (
	"beacon/internal/notifier"
	"context"

	"beacon/internal/models"

	"github.com/google/uuid"
)

type EmailActivites struct {
	EmailService notifier.Notifier[models.EmailMessage]
}

func (a *EmailActivites) SendEmailActivity(ctx context.Context, msg *models.EmailMessage) error {
	return a.EmailService.Send(ctx, &notifier.Message[models.EmailMessage]{
		ID:   uuid.NewString(),
		Type: notifier.EmailNotifier,
		Data: *msg,
	})
}
