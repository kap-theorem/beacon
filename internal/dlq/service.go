package dlq

import (
	"context"
	"log/slog"

	"go.temporal.io/sdk/client"
)

// DLQService provides query and replay operations over failed Temporal workflow executions.
type DLQService struct {
	tc        client.Client
	namespace string
	logger    *slog.Logger
}

func NewDLQService(tc client.Client, namespace string, logger *slog.Logger) *DLQService {
	return &DLQService{tc: tc, namespace: namespace, logger: logger}
}

// QueryFailures returns closed SendEmailWorkflow executions matching the filter (S4, S6).
func (s *DLQService) QueryFailures(ctx context.Context, filter FailureFilter) ([]*FailedNotification, error) {
	return queryFailedWorkflows(ctx, s.tc, s.namespace, filter, s.logger)
}

// ReplayWorkflow re-dispatches a failed workflow execution (S5).
func (s *DLQService) ReplayWorkflow(ctx context.Context, workflowID string) (*ReplayResult, error) {
	return replayWorkflow(ctx, s.tc, workflowID)
}
