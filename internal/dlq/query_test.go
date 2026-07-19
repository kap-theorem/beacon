package dlq

import (
	"context"
	"errors"
	"testing"
	"time"

	"beacon/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	historypb "go.temporal.io/api/history/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/mocks"
	"google.golang.org/protobuf/types/known/timestamppb"
	"log/slog"
)

// ---------- helpers ----------

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(nil, &slog.HandlerOptions{Level: slog.LevelError + 100}))
}

func mustPayloads(t *testing.T, msg *models.EmailMessage) *commonpb.Payloads {
	t.Helper()
	p, err := converter.GetDefaultDataConverter().ToPayloads(msg)
	require.NoError(t, err)
	return p
}

// mustNotificationPayloads encodes an envelope-shaped (v2) workflow input, as
// opposed to mustPayloads' legacy EmailMessage shape.
func mustNotificationPayloads(t *testing.T, n *models.Notification) *commonpb.Payloads {
	t.Helper()
	p, err := converter.GetDefaultDataConverter().ToPayloads(n)
	require.NoError(t, err)
	return p
}

func makeExecInfo(workflowID, runID, taskQueue string, status enumspb.WorkflowExecutionStatus, closeTime time.Time) *workflowpb.WorkflowExecutionInfo {
	return makeExecInfoWithMemo(workflowID, runID, taskQueue, status, closeTime, nil)
}

func makeExecInfoWithMemo(workflowID, runID, taskQueue string, status enumspb.WorkflowExecutionStatus, closeTime time.Time, memo *commonpb.Memo) *workflowpb.WorkflowExecutionInfo {
	return &workflowpb.WorkflowExecutionInfo{
		Execution: &commonpb.WorkflowExecution{
			WorkflowId: workflowID,
			RunId:      runID,
		},
		TaskQueue: taskQueue,
		Status:    status,
		CloseTime: timestamppb.New(closeTime),
		Memo:      memo,
	}
}

func makeListResp(infos ...*workflowpb.WorkflowExecutionInfo) *workflowservice.ListClosedWorkflowExecutionsResponse {
	return &workflowservice.ListClosedWorkflowExecutionsResponse{
		Executions: infos,
	}
}

// buildHistoryIter constructs a mock HistoryEventIterator from a slice of events.
func buildHistoryIter(events []*historypb.HistoryEvent) *mocks.HistoryEventIterator {
	iter := &mocks.HistoryEventIterator{}
	for _, ev := range events {
		iter.On("HasNext").Return(true).Once()
		iter.On("Next").Return(ev, nil).Once()
	}
	// Final HasNext returns false to end the loop.
	iter.On("HasNext").Return(false).Once()
	return iter
}

func emptyHistoryIter() *mocks.HistoryEventIterator {
	iter := &mocks.HistoryEventIterator{}
	iter.On("HasNext").Return(false).Once()
	return iter
}

func makeStartedEvent(payloads *commonpb.Payloads) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
			WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
				Input: payloads,
			},
		},
	}
}

func makeActivityFailedEvent(msg string, eventTime time.Time) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_FAILED,
		EventTime: timestamppb.New(eventTime),
		Attributes: &historypb.HistoryEvent_ActivityTaskFailedEventAttributes{
			ActivityTaskFailedEventAttributes: &historypb.ActivityTaskFailedEventAttributes{
				Failure: &failurepb.Failure{Message: msg},
			},
		},
	}
}

func makeWorkflowFailedEvent(msg string) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_FAILED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionFailedEventAttributes{
			WorkflowExecutionFailedEventAttributes: &historypb.WorkflowExecutionFailedEventAttributes{
				Failure: &failurepb.Failure{Message: msg},
			},
		},
	}
}

func makeWorkflowTimedOutEvent() *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventType:  enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_TIMED_OUT,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionTimedOutEventAttributes{},
	}
}

func makeWorkflowCanceledEvent() *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventType:  enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CANCELED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionCanceledEventAttributes{},
	}
}

// ---------- parseProviderFromTaskQueue ----------

func TestParseProviderFromTaskQueue(t *testing.T) {
	tests := []struct {
		tq       string
		expected string
	}{
		{"email-sendgrid-queue", "sendgrid"},
		{"email-mailgun-queue", "mailgun"},
		{"email-smtp-queue", "smtp"},
		{"email--queue", ""},     // empty provider segment
		{"sendgrid", "sendgrid"}, // no prefix/suffix
		{"", ""},
	}
	for _, tc := range tests {
		got := parseProviderFromTaskQueue(tc.tq)
		assert.Equal(t, tc.expected, got, "tq=%q", tc.tq)
	}
}

// ---------- workflowStatus ----------

func TestWorkflowStatus(t *testing.T) {
	assert.Equal(t, "Failed", workflowStatus(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED))
	assert.Equal(t, "TimedOut", workflowStatus(enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT))
	assert.Equal(t, "Canceled", workflowStatus(enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED))
	assert.Equal(t, "", workflowStatus(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING))
	assert.Equal(t, "", workflowStatus(enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED))
}

// ---------- statusMatches ----------

func TestStatusMatches_EmptyFilter(t *testing.T) {
	// Empty filter should include Failed, TimedOut, Canceled but not Running/Completed/""
	assert.True(t, statusMatches("Failed", ""))
	assert.True(t, statusMatches("TimedOut", ""))
	assert.True(t, statusMatches("Canceled", ""))
	assert.False(t, statusMatches("", "")) // unrecognised status
	assert.False(t, statusMatches("Running", ""))
}

func TestStatusMatches_SpecificFilter(t *testing.T) {
	// Case-insensitive matching
	assert.True(t, statusMatches("Failed", "Failed"))
	assert.True(t, statusMatches("Failed", "failed"))
	assert.True(t, statusMatches("Failed", "FAILED"))
	assert.False(t, statusMatches("TimedOut", "Failed"))
	assert.True(t, statusMatches("Canceled", "CANCELED"))
}

// ---------- extractWorkflowDetails ----------

func TestExtractWorkflowDetails_StartedAndFailed(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	msg := &models.EmailMessage{To: "user@example.com", Subject: "Hello", Body: "World"}
	payloads := mustPayloads(t, msg)
	failTime := time.Now().Add(-5 * time.Minute)

	events := []*historypb.HistoryEvent{
		makeStartedEvent(payloads),
		makeActivityFailedEvent("smtp timeout", failTime),
		makeWorkflowFailedEvent("max retries exceeded"),
	}
	iter := buildHistoryIter(events)
	mc.On("GetWorkflowHistory", ctx, "wf1", "run1", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	d, err := extractWorkflowDetails(ctx, mc, "wf1", "run1")
	require.NoError(t, err)
	assert.Equal(t, "user@example.com", d.recipient)
	assert.Equal(t, "Hello", d.subject)
	assert.Equal(t, "smtp timeout", d.failureReason) // activity failure sets it first
	assert.Equal(t, int32(1), d.retryCount)
	assert.WithinDuration(t, failTime, d.lastAttemptAt, time.Second)
	assert.NotNil(t, d.msg)
	mc.AssertExpectations(t)
}

func TestExtractWorkflowDetails_MultipleActivityFailures(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	msg := &models.EmailMessage{To: "a@b.com", Subject: "Test"}
	payloads := mustPayloads(t, msg)
	t1 := time.Now().Add(-10 * time.Minute)
	t2 := time.Now().Add(-5 * time.Minute)

	events := []*historypb.HistoryEvent{
		makeStartedEvent(payloads),
		makeActivityFailedEvent("first failure", t1),
		makeActivityFailedEvent("second failure", t2),
	}
	iter := buildHistoryIter(events)
	mc.On("GetWorkflowHistory", ctx, "wf2", "run2", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	d, err := extractWorkflowDetails(ctx, mc, "wf2", "run2")
	require.NoError(t, err)
	// failureReason set only once (first activity failure wins)
	assert.Equal(t, "first failure", d.failureReason)
	// retryCount increments for both
	assert.Equal(t, int32(2), d.retryCount)
	// lastAttemptAt is updated to the latest event
	assert.WithinDuration(t, t2, d.lastAttemptAt, time.Second)
	mc.AssertExpectations(t)
}

func TestExtractWorkflowDetails_TimedOut(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()

	events := []*historypb.HistoryEvent{
		makeWorkflowTimedOutEvent(),
	}
	iter := buildHistoryIter(events)
	mc.On("GetWorkflowHistory", ctx, "wf3", "run3", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	d, err := extractWorkflowDetails(ctx, mc, "wf3", "run3")
	require.NoError(t, err)
	assert.Equal(t, "workflow timed out", d.failureReason)
	mc.AssertExpectations(t)
}

func TestExtractWorkflowDetails_Canceled(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()

	events := []*historypb.HistoryEvent{
		makeWorkflowCanceledEvent(),
	}
	iter := buildHistoryIter(events)
	mc.On("GetWorkflowHistory", ctx, "wf4", "run4", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	d, err := extractWorkflowDetails(ctx, mc, "wf4", "run4")
	require.NoError(t, err)
	assert.Equal(t, "workflow canceled", d.failureReason)
	mc.AssertExpectations(t)
}

func TestExtractWorkflowDetails_EmptyHistory(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	iter := emptyHistoryIter()
	mc.On("GetWorkflowHistory", ctx, "wf5", "run5", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	d, err := extractWorkflowDetails(ctx, mc, "wf5", "run5")
	require.NoError(t, err)
	assert.Nil(t, d.msg)
	assert.Equal(t, "", d.recipient)
	assert.Equal(t, int32(0), d.retryCount)
	mc.AssertExpectations(t)
}

func TestExtractWorkflowDetails_IteratorError(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()

	iter := &mocks.HistoryEventIterator{}
	iter.On("HasNext").Return(true).Once()
	iter.On("Next").Return((*historypb.HistoryEvent)(nil), errors.New("grpc error")).Once()
	mc.On("GetWorkflowHistory", ctx, "wf6", "run6", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	_, err := extractWorkflowDetails(ctx, mc, "wf6", "run6")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read history event")
	mc.AssertExpectations(t)
}

func TestExtractWorkflowDetails_StartedEventNilInput(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()

	// Started event with nil input — decode is skipped, msg stays nil
	startedEvent := &historypb.HistoryEvent{
		EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
			WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
				Input: nil,
			},
		},
	}
	iter := buildHistoryIter([]*historypb.HistoryEvent{startedEvent})
	mc.On("GetWorkflowHistory", ctx, "wf7", "run7", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	d, err := extractWorkflowDetails(ctx, mc, "wf7", "run7")
	require.NoError(t, err)
	assert.Nil(t, d.msg)
	mc.AssertExpectations(t)
}

// ---------- queryFailedWorkflows ----------

func TestQueryFailedWorkflows_ReturnsAllStatusesWhenFilterEmpty(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	executions := []*workflowpb.WorkflowExecutionInfo{
		makeExecInfo("wf-failed", "r1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now),
		makeExecInfo("wf-timedout", "r2", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT, now),
		makeExecInfo("wf-canceled", "r3", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, now),
		// completed should be excluded
		makeExecInfo("wf-completed", "r4", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, now),
	}
	resp := makeListResp(executions...)

	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	// Setup empty history for each filtered workflow
	for _, wfID := range []string{"wf-failed", "wf-timedout", "wf-canceled"} {
		runID := ""
		switch wfID {
		case "wf-failed":
			runID = "r1"
		case "wf-timedout":
			runID = "r2"
		case "wf-canceled":
			runID = "r3"
		}
		iter := emptyHistoryIter()
		mc.On("GetWorkflowHistory", ctx, wfID, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)
	}

	filter := FailureFilter{Limit: 10}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, results, 3)

	statuses := make(map[string]bool)
	for _, r := range results {
		statuses[r.Status] = true
	}
	assert.True(t, statuses["Failed"])
	assert.True(t, statuses["TimedOut"])
	assert.True(t, statuses["Canceled"])
	mc.AssertExpectations(t)
}

func TestQueryFailedWorkflows_StatusFilterCaseInsensitive(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	executions := []*workflowpb.WorkflowExecutionInfo{
		makeExecInfo("wf-failed", "r1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now),
		makeExecInfo("wf-timedout", "r2", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT, now),
	}
	resp := makeListResp(executions...)
	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	// Only "wf-failed" should pass the filter; set up its history iterator
	iter := emptyHistoryIter()
	mc.On("GetWorkflowHistory", ctx, "wf-failed", "r1", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	filter := FailureFilter{Status: "failed"} // lowercase — should match "Failed"
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "Failed", results[0].Status)
	mc.AssertExpectations(t)
}

func TestQueryFailedWorkflows_ProviderFilter(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	executions := []*workflowpb.WorkflowExecutionInfo{
		makeExecInfo("wf1", "r1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now),
		makeExecInfo("wf2", "r2", "email-mailgun-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now),
	}
	resp := makeListResp(executions...)
	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	// Only "wf1" passes the provider filter
	iter := emptyHistoryIter()
	mc.On("GetWorkflowHistory", ctx, "wf1", "r1", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	filter := FailureFilter{Provider: "sendgrid"}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "sendgrid", results[0].Provider)
	mc.AssertExpectations(t)
}

func TestQueryFailedWorkflows_LimitClamping(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	// Build 5 failed executions
	var executions []*workflowpb.WorkflowExecutionInfo
	for i := 0; i < 5; i++ {
		wfID := "wf-" + string(rune('A'+i))
		executions = append(executions, makeExecInfo(wfID, "r"+string(rune('A'+i)), "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now))
	}
	resp := makeListResp(executions...)
	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	for _, exec := range executions {
		iter := emptyHistoryIter()
		mc.On("GetWorkflowHistory", ctx, exec.Execution.WorkflowId, exec.Execution.RunId, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)
	}

	// Limit > 100 should be clamped to 100 (but we have only 5 results)
	filter := FailureFilter{Limit: 200}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, results, 5) // only 5 available
	mc.AssertExpectations(t)
}

func TestQueryFailedWorkflows_DefaultLimit(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	// Build 25 failed executions (more than default limit of 20)
	var executions []*workflowpb.WorkflowExecutionInfo
	for i := 0; i < 25; i++ {
		wfID := "wf-" + string(rune('a'+i%26))
		executions = append(executions, makeExecInfo(wfID, "rr"+string(rune('a'+i%26)), "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now))
	}
	resp := makeListResp(executions...)
	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	// Only the first page (default limit of 20) has its history fetched.
	for _, exec := range executions[:20] {
		iter := emptyHistoryIter()
		mc.On("GetWorkflowHistory", ctx, exec.Execution.WorkflowId, exec.Execution.RunId, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)
	}

	// Limit <= 0 should default to 20
	filter := FailureFilter{Limit: 0}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, results, 20)
	mc.AssertExpectations(t)
}

func TestQueryFailedWorkflows_OffsetBeyondResults(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	executions := []*workflowpb.WorkflowExecutionInfo{
		makeExecInfo("wf1", "r1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now),
	}
	resp := makeListResp(executions...)
	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	// No history is fetched: the offset skips past every match, so no
	// execution makes it into the returned page.

	// Offset beyond available results should return empty slice
	filter := FailureFilter{Limit: 10, Offset: 100}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	assert.Empty(t, results)
	mc.AssertExpectations(t)
}

func TestQueryFailedWorkflows_NegativeOffsetClampedToZero(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	executions := []*workflowpb.WorkflowExecutionInfo{
		makeExecInfo("wf1", "run1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now),
	}
	resp := makeListResp(executions...)
	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	msg := &models.EmailMessage{To: "user@example.com", Subject: "Hello", Body: "World"}
	iter := buildHistoryIter([]*historypb.HistoryEvent{makeStartedEvent(mustPayloads(t, msg))})
	mc.On("GetWorkflowHistory", ctx, "wf1", "run1", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	// A negative offset must behave like offset 0, not panic on the slice.
	filter := FailureFilter{Limit: 10, Offset: -5}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "wf1", results[0].WorkflowID)
	mc.AssertExpectations(t)
}

func TestQueryFailedWorkflows_ListClosedWorkflowError(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()

	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(
		(*workflowservice.ListClosedWorkflowExecutionsResponse)(nil),
		errors.New("temporal unavailable"),
	)

	filter := FailureFilter{}
	_, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list closed workflows")
	mc.AssertExpectations(t)
}

func TestQueryFailedWorkflows_ExtractDetailsError_UsesEmptyDetails(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	executions := []*workflowpb.WorkflowExecutionInfo{
		makeExecInfo("wf1", "r1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now),
	}
	resp := makeListResp(executions...)
	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	// Return iterator that errors on Next
	iter := &mocks.HistoryEventIterator{}
	iter.On("HasNext").Return(true).Once()
	iter.On("Next").Return((*historypb.HistoryEvent)(nil), errors.New("history error")).Once()
	mc.On("GetWorkflowHistory", ctx, "wf1", "r1", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	// The service logs the warning and continues with empty details
	filter := FailureFilter{Limit: 10}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	// details should be empty
	assert.Equal(t, "", results[0].Recipient)
	assert.Equal(t, "", results[0].FailureReason)
	mc.AssertExpectations(t)
}

func TestQueryFailedWorkflows_WithLastAttemptAt(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()
	failTime := now.Add(-3 * time.Minute)

	executions := []*workflowpb.WorkflowExecutionInfo{
		makeExecInfo("wf1", "r1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now),
	}
	resp := makeListResp(executions...)
	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	msg := &models.EmailMessage{To: "x@y.com", Subject: "sub"}
	payloads := mustPayloads(t, msg)
	events := []*historypb.HistoryEvent{
		makeStartedEvent(payloads),
		makeActivityFailedEvent("err", failTime),
	}
	iter := buildHistoryIter(events)
	mc.On("GetWorkflowHistory", ctx, "wf1", "r1", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	filter := FailureFilter{Limit: 10}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.WithinDuration(t, failTime, results[0].LastAttemptAt, time.Second)
	assert.Equal(t, "x@y.com", results[0].Recipient)
	assert.Equal(t, "sub", results[0].Subject)
	mc.AssertExpectations(t)
}

func TestQueryFailedWorkflows_PaginationOffset(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	executions := []*workflowpb.WorkflowExecutionInfo{
		makeExecInfo("wf1", "r1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now),
		makeExecInfo("wf2", "r2", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now),
		makeExecInfo("wf3", "r3", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now),
	}
	resp := makeListResp(executions...)
	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	// Only the returned page (offset 1 onwards) has its history fetched.
	for _, exec := range executions[1:] {
		iter := emptyHistoryIter()
		mc.On("GetWorkflowHistory", ctx, exec.Execution.WorkflowId, exec.Execution.RunId, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)
	}

	filter := FailureFilter{Limit: 10, Offset: 1}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	// 3 results minus offset 1 = 2
	assert.Len(t, results, 2)
	assert.Equal(t, "wf2", results[0].WorkflowID)
	mc.AssertExpectations(t)
}

// ---------- DLQService.QueryFailures (via service) ----------

func TestDLQService_QueryFailures_IntegrationPath(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	executions := []*workflowpb.WorkflowExecutionInfo{
		makeExecInfo("wf1", "r1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now),
	}
	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(makeListResp(executions...), nil)
	mc.On("GetWorkflowHistory", ctx, "wf1", "r1", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(emptyHistoryIter())

	svc := NewDLQService(mc, "default", noopLogger())
	results, err := svc.QueryFailures(ctx, FailureFilter{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, results, 1)
	mc.AssertExpectations(t)
}

// ---------- memo-based tenant/service/provider filtering (Task 10) ----------

// TestQueryFailedWorkflows_EnvelopeFormatHistory closes a coverage gap from
// Task 8: legacy-shaped (EmailMessage) histories were tested but envelope
// (models.Notification) shaped histories, which is what v2 /v1/notify
// workflows actually record, were not.
func TestQueryFailedWorkflows_EnvelopeFormatHistory(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	executions := []*workflowpb.WorkflowExecutionInfo{
		makeExecInfo("wf-envelope", "r1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now),
	}
	resp := makeListResp(executions...)
	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	n := &models.Notification{
		Channel: "email", Service: "billing-api", Tenant: "payments",
		Email: &models.EmailPayload{To: "v2@x.com", Subject: "v2subj", Body: "b"},
	}
	payloads := mustNotificationPayloads(t, n)
	iter := buildHistoryIter([]*historypb.HistoryEvent{makeStartedEvent(payloads)})
	mc.On("GetWorkflowHistory", ctx, "wf-envelope", "r1", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	filter := FailureFilter{Limit: 10}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "v2@x.com", results[0].Recipient)
	assert.Equal(t, "v2subj", results[0].Subject)
	mc.AssertExpectations(t)
}

// TestQueryFailedWorkflows_TenantFilter proves filter.Tenant scopes results to
// the matching workflow's memo tenant only, and that Service/Tenant are
// populated on the returned FailedNotification from the memo.
func TestQueryFailedWorkflows_TenantFilter(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	memoPayments := memoFixture(t, map[string]string{"tenant": "payments", "service": "billing-api"})
	memoObs := memoFixture(t, map[string]string{"tenant": "obs", "service": "metrics-agent"})

	executions := []*workflowpb.WorkflowExecutionInfo{
		makeExecInfoWithMemo("wf-payments", "r1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now, memoPayments),
		makeExecInfoWithMemo("wf-obs", "r2", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now, memoObs),
	}
	resp := makeListResp(executions...)
	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	// Only "wf-payments" passes the tenant filter, so only its history is fetched.
	iter := emptyHistoryIter()
	mc.On("GetWorkflowHistory", ctx, "wf-payments", "r1", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	filter := FailureFilter{Tenant: "payments", Limit: 10}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "wf-payments", results[0].WorkflowID)
	assert.Equal(t, "payments", results[0].Tenant)
	assert.Equal(t, "billing-api", results[0].Service)
	mc.AssertExpectations(t)
}

// ---------- pagination follow-through across ListClosedWorkflow pages ----------

// TestQueryFailures_FollowsPagesUntilTenantMatchesFound proves that when a
// tenant filter discards an entire page of results, QueryFailures keeps
// following NextPageToken (instead of returning what looks like "no
// failures") until it finds a page containing matches for the tenant.
func TestQueryFailures_FollowsPagesUntilTenantMatchesFound(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	memoOther := memoFixture(t, map[string]string{"tenant": "other"})
	memoPayments := memoFixture(t, map[string]string{"tenant": "payments"})

	page1 := &workflowservice.ListClosedWorkflowExecutionsResponse{
		Executions: []*workflowpb.WorkflowExecutionInfo{
			makeExecInfoWithMemo("wf-other-1", "r1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now, memoOther),
			makeExecInfoWithMemo("wf-other-2", "r2", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now, memoOther),
		},
		NextPageToken: []byte("page-2"),
	}
	page2 := &workflowservice.ListClosedWorkflowExecutionsResponse{
		Executions: []*workflowpb.WorkflowExecutionInfo{
			makeExecInfoWithMemo("wf-payments-1", "r3", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now, memoPayments),
		},
		NextPageToken: nil,
	}

	mc.On("ListClosedWorkflow", ctx, mock.MatchedBy(func(req *workflowservice.ListClosedWorkflowExecutionsRequest) bool {
		return len(req.NextPageToken) == 0
	})).Return(page1, nil).Once()
	mc.On("ListClosedWorkflow", ctx, mock.MatchedBy(func(req *workflowservice.ListClosedWorkflowExecutionsRequest) bool {
		return string(req.NextPageToken) == "page-2"
	})).Return(page2, nil).Once()

	iter := emptyHistoryIter()
	mc.On("GetWorkflowHistory", ctx, "wf-payments-1", "r3", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	filter := FailureFilter{Tenant: "payments", Limit: 10}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "wf-payments-1", results[0].WorkflowID)
	assert.Equal(t, "payments", results[0].Tenant)
	mc.AssertNumberOfCalls(t, "ListClosedWorkflow", 2)
	mc.AssertExpectations(t)
}

// TestQueryFailures_StopsAtPageCap proves the pagination loop is bounded: if
// every page returns a NextPageToken but never a matching tenant, the loop
// stops after maxListPages calls rather than paging indefinitely.
func TestQueryFailures_StopsAtPageCap(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	memoOther := memoFixture(t, map[string]string{"tenant": "other"})
	resp := &workflowservice.ListClosedWorkflowExecutionsResponse{
		Executions: []*workflowpb.WorkflowExecutionInfo{
			makeExecInfoWithMemo("wf-other", "r1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now, memoOther),
		},
		NextPageToken: []byte("more"),
	}

	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	filter := FailureFilter{Tenant: "payments", Limit: 10}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	assert.Empty(t, results)
	mc.AssertNumberOfCalls(t, "ListClosedWorkflow", maxListPages)
}

// TestQueryFailedWorkflows_MemoProviderOverridesTaskQueueParse proves that
// when a memo "provider" field is present it wins over the task-queue-parsed
// provider (which stays as a fallback for pre-Task-9 workflows with no memo).
func TestQueryFailedWorkflows_MemoProviderOverridesTaskQueueParse(t *testing.T) {
	mc := &mocks.Client{}
	ctx := context.Background()
	now := time.Now()

	memo := memoFixture(t, map[string]string{"provider": "mailgun-eu"})
	executions := []*workflowpb.WorkflowExecutionInfo{
		makeExecInfoWithMemo("wf-memo-provider", "r1", "email-sendgrid-queue", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, now, memo),
	}
	resp := makeListResp(executions...)
	mc.On("ListClosedWorkflow", ctx, mock.Anything).Return(resp, nil)

	iter := emptyHistoryIter()
	mc.On("GetWorkflowHistory", ctx, "wf-memo-provider", "r1", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT).Return(iter)

	filter := FailureFilter{Limit: 10}
	results, err := NewDLQService(mc, "default", noopLogger()).QueryFailures(ctx, filter)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "mailgun-eu", results[0].Provider)
	mc.AssertExpectations(t)
}
