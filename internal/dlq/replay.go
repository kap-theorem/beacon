package dlq

import (
	"beacon/internal/notifier"
	"beacon/internal/temporal"
	"context"
	"errors"
	"fmt"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
)

// ErrNotTerminalState is returned when a replay is requested for a workflow that is still running.
var ErrNotTerminalState = errors.New("workflow is not in a terminal state")

// ErrWorkflowNotFound is returned when the workflow ID does not exist in Temporal.
var ErrWorkflowNotFound = errors.New("workflow not found")

// replayWorkflow fetches the original workflow input and dispatches a new SendEmailWorkflow execution.
func replayWorkflow(ctx context.Context, tc client.Client, workflowID string) (*ReplayResult, error) {
	descResp, err := tc.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrWorkflowNotFound, workflowID)
	}

	info := descResp.WorkflowExecutionInfo
	runID := info.Execution.RunId
	taskQueue := info.TaskQueue

	switch info.Status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
		enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
		enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:
		// terminal state — safe to replay
	default:
		return nil, ErrNotTerminalState
	}

	details, err := extractWorkflowDetails(ctx, tc, workflowID, runID)
	if err != nil {
		return nil, fmt.Errorf("extract workflow input from history: %w", err)
	}
	if details.msg == nil {
		return nil, fmt.Errorf("original workflow input not found in history for %s", workflowID)
	}

	provider := parseProviderFromTaskQueue(taskQueue)
	replayQueue := notifier.TaskQueueFor(provider)
	newWorkflowID := fmt.Sprintf("replay-%s-%d", workflowID, time.Now().UnixNano())

	run, err := tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        newWorkflowID,
		TaskQueue: replayQueue,
	}, temporal.SendEmailWorkflow, details.msg)
	if err != nil {
		return nil, fmt.Errorf("dispatch replay workflow: %w", err)
	}

	return &ReplayResult{
		NewWorkflowID:      run.GetID(),
		NewRunID:           run.GetRunID(),
		OriginalWorkflowID: workflowID,
		Provider:           provider,
	}, nil
}
