package dlq

import (
	"context"
	"errors"
	"testing"
	"time"

	"beacon/internal/models"
	"beacon/internal/notifier"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/mocks"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// describeResp builds a DescribeWorkflowExecutionResponse with the given status and task queue.
func describeResp(status enumspb.WorkflowExecutionStatus, taskQueue, workflowID, runID string) *workflowservice.DescribeWorkflowExecutionResponse {
	return describeRespWithMemo(status, taskQueue, workflowID, runID, nil)
}

// describeRespWithMemo is describeResp plus an explicit workflow memo, used by
// tenant-scoping tests.
func describeRespWithMemo(status enumspb.WorkflowExecutionStatus, taskQueue, workflowID, runID string, memo *commonpb.Memo) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{
				WorkflowId: workflowID,
				RunId:      runID,
			},
			Type:      &commonpb.WorkflowType{Name: "SendEmailWorkflow"},
			TaskQueue: taskQueue,
			Status:    status,
			CloseTime: timestamppb.New(time.Now()),
			Memo:      memo,
		},
	}
}

// ---------- replayWorkflow error paths ----------

func TestReplay_WorkflowNotFound(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()

	mc.On("DescribeWorkflowExecution", ctx, "wf-missing", "").
		Return((*workflowservice.DescribeWorkflowExecutionResponse)(nil), errors.New("not found"))

	_, err := NewDLQService(mc, "default", noopLogger()).ReplayWorkflow(ctx, "wf-missing", "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkflowNotFound), "expected ErrWorkflowNotFound, got %v", err)
	mc.AssertExpectations(t)
}

func TestReplay_NotTerminal_Running(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()

	mc.On("DescribeWorkflowExecution", ctx, "wf1", "").
		Return(describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, "email-sendgrid-queue", "wf1", "run1"), nil)

	_, err := NewDLQService(mc, "default", noopLogger()).ReplayWorkflow(ctx, "wf1", "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotTerminalState), "expected ErrNotTerminalState, got %v", err)
	mc.AssertExpectations(t)
}

func TestReplay_NotTerminal_Completed(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()

	mc.On("DescribeWorkflowExecution", ctx, "wf2", "").
		Return(describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, "email-sendgrid-queue", "wf2", "run2"), nil)

	_, err := NewDLQService(mc, "default", noopLogger()).ReplayWorkflow(ctx, "wf2", "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotTerminalState), "expected ErrNotTerminalState, got %v", err)
	mc.AssertExpectations(t)
}

func TestReplay_HistoryWithoutInput_ReturnsError(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()

	mc.On("DescribeWorkflowExecution", ctx, "wf3", "").
		Return(describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "email-sendgrid-queue", "wf3", "run3"), nil)

	// History has no WorkflowExecutionStarted with input, so msg remains nil
	iter := emptyHistoryIter()
	mc.On("GetWorkflowHistory", ctx, "wf3", "run3", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	_, err := NewDLQService(mc, "default", noopLogger()).ReplayWorkflow(ctx, "wf3", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "input not found")
	mc.AssertExpectations(t)
}

// ---------- terminal states that allow replay ----------

func TestReplay_TerminalFailed_Success(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	origWFID := "wf-orig"
	origRunID := "run-orig"

	mc.On("DescribeWorkflowExecution", ctx, origWFID, "").
		Return(describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "email-sendgrid-queue", origWFID, origRunID), nil)

	msg := &models.EmailMessage{To: "r@example.com", Subject: "sub", Body: "body"}
	payloads := mustPayloads(t, msg)
	events := []*historypb.HistoryEvent{makeStartedEvent(payloads)}
	iter := buildHistoryIter(events)
	mc.On("GetWorkflowHistory", ctx, origWFID, origRunID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	expectedNewID := "replay-" + origWFID
	expectedQueue := notifier.TaskQueueFor("sendgrid")

	wfRun := &mocks.WorkflowRun{}
	wfRun.On("GetID").Return(expectedNewID)
	wfRun.On("GetRunID").Return("new-run-123")

	mc.On("ExecuteWorkflow",
		ctx,
		mock.MatchedBy(func(opts client.StartWorkflowOptions) bool {
			return opts.ID == expectedNewID &&
				opts.TaskQueue == expectedQueue &&
				opts.WorkflowIDReusePolicy == enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY
		}),
		"SendEmailWorkflow",
		mock.Anything,
	).Return(wfRun, nil)

	result, err := NewDLQService(mc, "default", noopLogger()).ReplayWorkflow(ctx, origWFID, "")
	require.NoError(t, err)
	assert.Equal(t, expectedNewID, result.NewWorkflowID)
	assert.Equal(t, "new-run-123", result.NewRunID)
	assert.Equal(t, origWFID, result.OriginalWorkflowID)
	assert.Equal(t, "sendgrid", result.Provider)
	mc.AssertExpectations(t)
	wfRun.AssertExpectations(t)
}

func TestReplay_TerminalTimedOut_Success(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	origWFID := "wf-timedout"
	origRunID := "run-timedout"

	mc.On("DescribeWorkflowExecution", ctx, origWFID, "").
		Return(describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT, "email-mailgun-queue", origWFID, origRunID), nil)

	msg := &models.EmailMessage{To: "t@example.com", Subject: "timed"}
	payloads := mustPayloads(t, msg)
	events := []*historypb.HistoryEvent{makeStartedEvent(payloads)}
	iter := buildHistoryIter(events)
	mc.On("GetWorkflowHistory", ctx, origWFID, origRunID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	wfRun := &mocks.WorkflowRun{}
	wfRun.On("GetID").Return("replay-" + origWFID)
	wfRun.On("GetRunID").Return("new-run-456")

	mc.On("ExecuteWorkflow", ctx, mock.Anything,
		"SendEmailWorkflow",
		mock.Anything,
	).Return(wfRun, nil)

	result, err := NewDLQService(mc, "default", noopLogger()).ReplayWorkflow(ctx, origWFID, "")
	require.NoError(t, err)
	assert.Equal(t, "mailgun", result.Provider)
	mc.AssertExpectations(t)
}

func TestReplay_TerminalCanceled_Success(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	origWFID := "wf-canceled"
	origRunID := "run-canceled"

	mc.On("DescribeWorkflowExecution", ctx, origWFID, "").
		Return(describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, "email-smtp-queue", origWFID, origRunID), nil)

	msg := &models.EmailMessage{To: "c@example.com", Subject: "can"}
	payloads := mustPayloads(t, msg)
	events := []*historypb.HistoryEvent{makeStartedEvent(payloads)}
	iter := buildHistoryIter(events)
	mc.On("GetWorkflowHistory", ctx, origWFID, origRunID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	wfRun := &mocks.WorkflowRun{}
	wfRun.On("GetID").Return("replay-" + origWFID)
	wfRun.On("GetRunID").Return("new-run-789")

	mc.On("ExecuteWorkflow", ctx, mock.Anything,
		"SendEmailWorkflow",
		mock.Anything,
	).Return(wfRun, nil)

	result, err := NewDLQService(mc, "default", noopLogger()).ReplayWorkflow(ctx, origWFID, "")
	require.NoError(t, err)
	assert.Equal(t, "smtp", result.Provider)
	mc.AssertExpectations(t)
}

// ---------- ErrReplayAlreadyRunning ----------

func TestReplay_AlreadyStarted(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	origWFID := "wf-dup"
	origRunID := "run-dup"

	mc.On("DescribeWorkflowExecution", ctx, origWFID, "").
		Return(describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "email-sendgrid-queue", origWFID, origRunID), nil)

	msg := &models.EmailMessage{To: "d@example.com", Subject: "dup"}
	payloads := mustPayloads(t, msg)
	events := []*historypb.HistoryEvent{makeStartedEvent(payloads)}
	iter := buildHistoryIter(events)
	mc.On("GetWorkflowHistory", ctx, origWFID, origRunID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	// Simulate already-started error from Temporal
	alreadyStartedErr := serviceerror.NewWorkflowExecutionAlreadyStarted(
		"already running", "req-id-1", "existing-run-id",
	)
	mc.On("ExecuteWorkflow", ctx, mock.Anything,
		"SendEmailWorkflow",
		mock.Anything,
	).Return((*mocks.WorkflowRun)(nil), alreadyStartedErr)

	_, err := NewDLQService(mc, "default", noopLogger()).ReplayWorkflow(ctx, origWFID, "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrReplayAlreadyRunning), "expected ErrReplayAlreadyRunning, got %v", err)
	mc.AssertExpectations(t)
}

// ---------- ExecuteWorkflow generic error ----------

func TestReplay_ExecuteWorkflowError(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	origWFID := "wf-exec-err"
	origRunID := "run-exec-err"

	mc.On("DescribeWorkflowExecution", ctx, origWFID, "").
		Return(describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "email-sendgrid-queue", origWFID, origRunID), nil)

	msg := &models.EmailMessage{To: "e@example.com", Subject: "exec-err"}
	payloads := mustPayloads(t, msg)
	events := []*historypb.HistoryEvent{makeStartedEvent(payloads)}
	iter := buildHistoryIter(events)
	mc.On("GetWorkflowHistory", ctx, origWFID, origRunID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	mc.On("ExecuteWorkflow", ctx, mock.Anything,
		"SendEmailWorkflow",
		mock.Anything,
	).Return((*mocks.WorkflowRun)(nil), errors.New("temporal cluster unreachable"))

	_, err := NewDLQService(mc, "default", noopLogger()).ReplayWorkflow(ctx, origWFID, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dispatch replay workflow")
	mc.AssertExpectations(t)
}

// ---------- extractWorkflowDetails with activity + workflow failure in replay path ----------

func TestReplay_HistoryWithActivityFailure(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	origWFID := "wf-act-fail"
	origRunID := "run-act-fail"

	mc.On("DescribeWorkflowExecution", ctx, origWFID, "").
		Return(describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "email-sendgrid-queue", origWFID, origRunID), nil)

	msg := &models.EmailMessage{To: "af@example.com", Subject: "activity fail"}
	payloads := mustPayloads(t, msg)
	events := []*historypb.HistoryEvent{
		makeStartedEvent(payloads),
		{
			EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_FAILED,
			EventTime: timestamppb.New(time.Now()),
			Attributes: &historypb.HistoryEvent_ActivityTaskFailedEventAttributes{
				ActivityTaskFailedEventAttributes: &historypb.ActivityTaskFailedEventAttributes{
					Failure: &failurepb.Failure{Message: "smtp error"},
				},
			},
		},
	}
	iter := buildHistoryIter(events)
	mc.On("GetWorkflowHistory", ctx, origWFID, origRunID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	wfRun := &mocks.WorkflowRun{}
	wfRun.On("GetID").Return("replay-" + origWFID)
	wfRun.On("GetRunID").Return("new-run-af")

	mc.On("ExecuteWorkflow", ctx, mock.Anything,
		"SendEmailWorkflow",
		mock.Anything,
	).Return(wfRun, nil)

	result, err := NewDLQService(mc, "default", noopLogger()).ReplayWorkflow(ctx, origWFID, "")
	require.NoError(t, err)
	assert.Equal(t, "sendgrid", result.Provider)
	mc.AssertExpectations(t)
}

// ---------- DLQService.ReplayWorkflow (integration path) ----------

func TestDLQService_ReplayWorkflow_IntegrationPath(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	origWFID := "svc-wf-orig"
	origRunID := "svc-run-orig"

	mc.On("DescribeWorkflowExecution", ctx, origWFID, "").
		Return(describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "email-sendgrid-queue", origWFID, origRunID), nil)

	msg := &models.EmailMessage{To: "svc@example.com", Subject: "svc test"}
	payloads := mustPayloads(t, msg)
	events := []*historypb.HistoryEvent{makeStartedEvent(payloads)}
	iter := buildHistoryIter(events)
	mc.On("GetWorkflowHistory", ctx, origWFID, origRunID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	expectedNewID := "replay-" + origWFID
	wfRun := &mocks.WorkflowRun{}
	wfRun.On("GetID").Return(expectedNewID)
	wfRun.On("GetRunID").Return("svc-new-run")

	mc.On("ExecuteWorkflow", ctx, mock.Anything,
		"SendEmailWorkflow",
		mock.Anything,
	).Return(wfRun, nil)

	svc := NewDLQService(mc, "default", noopLogger())
	result, err := svc.ReplayWorkflow(ctx, origWFID, "")
	require.NoError(t, err)
	assert.Equal(t, expectedNewID, result.NewWorkflowID)
	assert.Equal(t, origWFID, result.OriginalWorkflowID)
	mc.AssertExpectations(t)
}

// ---------- tenant scoping ----------

func TestReplay_TenantMismatch_NotFound(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	origWFID := "wf-other-tenant"
	origRunID := "run-other-tenant"

	memo := memoFixture(t, map[string]string{"tenant": "other"})
	mc.On("DescribeWorkflowExecution", ctx, origWFID, "").
		Return(describeRespWithMemo(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "email-sendgrid-queue", origWFID, origRunID, memo), nil)

	_, err := NewDLQService(mc, "default", noopLogger()).ReplayWorkflow(ctx, origWFID, "payments")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkflowNotFound), "expected ErrWorkflowNotFound, got %v", err)
	// Existence must not be disclosed: no history lookup or dispatch should happen.
	mc.AssertNotCalled(t, "GetWorkflowHistory", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	mc.AssertNotCalled(t, "ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	mc.AssertExpectations(t)
}

func TestReplay_TenantMatch_Proceeds(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	origWFID := "wf-own-tenant"
	origRunID := "run-own-tenant"

	memo := memoFixture(t, map[string]string{"tenant": "payments", "service": "billing-api", "provider": "sendgrid"})
	mc.On("DescribeWorkflowExecution", ctx, origWFID, "").
		Return(describeRespWithMemo(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "email-sendgrid-queue", origWFID, origRunID, memo), nil)

	msg := &models.EmailMessage{To: "tenant@example.com", Subject: "tenant match"}
	payloads := mustPayloads(t, msg)
	iter := buildHistoryIter([]*historypb.HistoryEvent{makeStartedEvent(payloads)})
	mc.On("GetWorkflowHistory", ctx, origWFID, origRunID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	wfRun := &mocks.WorkflowRun{}
	wfRun.On("GetID").Return("replay-" + origWFID)
	wfRun.On("GetRunID").Return("new-run-tenant")

	mc.On("ExecuteWorkflow",
		ctx,
		mock.MatchedBy(func(opts client.StartWorkflowOptions) bool {
			return opts.Memo["tenant"] == "payments" && opts.Memo["service"] == "billing-api"
		}),
		"SendEmailWorkflow",
		mock.Anything,
	).Return(wfRun, nil)

	result, err := NewDLQService(mc, "default", noopLogger()).ReplayWorkflow(ctx, origWFID, "payments")
	require.NoError(t, err)
	assert.Equal(t, "sendgrid", result.Provider)
	mc.AssertExpectations(t)
}

func TestReplay_AdminUnscoped_ProceedsRegardlessOfTenant(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	origWFID := "wf-admin-view"
	origRunID := "run-admin-view"

	memo := memoFixture(t, map[string]string{"tenant": "other-tenant"})
	mc.On("DescribeWorkflowExecution", ctx, origWFID, "").
		Return(describeRespWithMemo(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "email-sendgrid-queue", origWFID, origRunID, memo), nil)

	msg := &models.EmailMessage{To: "admin@example.com", Subject: "admin view"}
	payloads := mustPayloads(t, msg)
	iter := buildHistoryIter([]*historypb.HistoryEvent{makeStartedEvent(payloads)})
	mc.On("GetWorkflowHistory", ctx, origWFID, origRunID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	wfRun := &mocks.WorkflowRun{}
	wfRun.On("GetID").Return("replay-" + origWFID)
	wfRun.On("GetRunID").Return("new-run-admin")

	mc.On("ExecuteWorkflow", ctx, mock.Anything,
		"SendEmailWorkflow",
		mock.Anything,
	).Return(wfRun, nil)

	// Empty callerTenant ("" = admin/unscoped) bypasses the tenant check entirely.
	_, err := NewDLQService(mc, "default", noopLogger()).ReplayWorkflow(ctx, origWFID, "")
	require.NoError(t, err)
	mc.AssertExpectations(t)
}
