package temporal

import (
	"beacon/internal/models"
	"time"

	"go.temporal.io/sdk/workflow"
)

func SendEmailWorkflow(ctx workflow.Context, req *models.EmailMessage) error {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute * 2,
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	emailActivites := &EmailActivites{}
	emailMessage := &models.EmailMessage{
		To:      req.To,
		Subject: req.Subject,
		Body:    req.Body,
	}
	return workflow.ExecuteActivity(ctx, emailActivites.SendEmailActivity, emailMessage).Get(ctx, nil)
}
