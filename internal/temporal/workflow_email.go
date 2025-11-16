package temporal

import (
	"beacon/internal/models"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func SendEmailWorkflow(ctx workflow.Context, msg *models.EmailMessage) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second * 5,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute * 2,
			MaximumAttempts:    3,
		},
		StartToCloseTimeout: time.Minute * 2,
	})
	return workflow.ExecuteActivity(ctx, "SendEmailActivity", msg).Get(ctx, nil)
}
