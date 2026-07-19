package dlq

import (
	"context"
	"errors"
	"fmt"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	temporalerr "go.temporal.io/sdk/temporal"
)

// ErrNotTerminalState is returned when a replay is requested for a workflow that is still running.
var ErrNotTerminalState = errors.New("workflow is not in a terminal state")

// ErrWorkflowNotFound is returned when the workflow ID does not exist in Temporal.
var ErrWorkflowNotFound = errors.New("workflow not found")

// ErrReplayAlreadyRunning is returned when a replay workflow for the given ID is already in progress.
var ErrReplayAlreadyRunning = errors.New("replay already in progress for this workflow")

// ReplayWorkflow re-dispatches a terminal failed workflow. When callerTenant
// is non-empty, the original workflow must belong to that tenant; mismatches
// return ErrWorkflowNotFound (existence is not disclosed across tenants).
func (s *DLQService) ReplayWorkflow(ctx context.Context, workflowID, callerTenant string) (*ReplayResult, error) {
	descResp, err := s.tc.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrWorkflowNotFound, workflowID)
	}

	info := descResp.WorkflowExecutionInfo
	runID := info.Execution.RunId

	if callerTenant != "" && memoString(info.Memo, "tenant") != callerTenant {
		return nil, fmt.Errorf("%w: %s", ErrWorkflowNotFound, workflowID)
	}

	switch info.Status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
		enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
		enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:
		// terminal state — safe to replay
	default:
		return nil, ErrNotTerminalState
	}

	details, err := extractWorkflowDetails(ctx, s.tc, workflowID, runID)
	if err != nil {
		return nil, fmt.Errorf("extract workflow input from history: %w", err)
	}
	if details.msg == nil {
		return nil, fmt.Errorf("original workflow input not found in history for %s", workflowID)
	}
	if details.msg.Email == nil {
		return nil, fmt.Errorf("workflow %s input decoded but has no email payload; replay for non-email channels is not supported yet", workflowID)
	}

	// Use a deterministic replay ID so Temporal itself rejects duplicate starts.
	newWorkflowID := fmt.Sprintf("replay-%s", workflowID)

	run, err := s.tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                    newWorkflowID,
		TaskQueue:             info.TaskQueue,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		Memo:                  memoToMap(info.Memo),
	}, info.Type.Name, details.msg)
	if err != nil {
		if temporalerr.IsWorkflowExecutionAlreadyStartedError(err) {
			return nil, ErrReplayAlreadyRunning
		}
		return nil, fmt.Errorf("dispatch replay workflow: %w", err)
	}

	provider := memoString(info.Memo, "provider")
	if provider == "" {
		provider = parseProviderFromTaskQueue(info.TaskQueue)
	}

	return &ReplayResult{
		NewWorkflowID:      run.GetID(),
		NewRunID:           run.GetRunID(),
		OriginalWorkflowID: workflowID,
		Provider:           provider,
	}, nil
}
