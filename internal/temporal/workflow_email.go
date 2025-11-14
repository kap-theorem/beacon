package temporal

import (
	"beacon/internal/models"
	"time"

	"go.temporal.io/sdk/workflow"
)

func SendEmailWorkflow(ctx workflow.Context, msg *models.EmailMessage) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute * 2,
	})
	return workflow.ExecuteActivity(ctx, "SendEmailActivity", msg).Get(ctx, nil)
}
