package dlq

import (
	"beacon/internal/models"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	filterpb "go.temporal.io/api/filter/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// queryFailedWorkflows lists closed SendEmailWorkflow executions and applies in-process filtering.
// Uses ListClosedWorkflow (no Elasticsearch required) with TypeFilter, then filters by status/provider/date.
func queryFailedWorkflows(ctx context.Context, tc client.Client, namespace string, filter FailureFilter, logger *slog.Logger) ([]*FailedNotification, error) {
	from := filter.FromDate
	to := filter.ToDate
	if from.IsZero() {
		from = time.Now().Add(-30 * 24 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	// Fetch more than needed so in-process filters still honour the requested limit.
	fetchSize := int32(limit+filter.Offset) * 3
	if fetchSize < 50 {
		fetchSize = 50
	}

	resp, err := tc.ListClosedWorkflow(ctx, &workflowservice.ListClosedWorkflowExecutionsRequest{
		Namespace:       namespace,
		MaximumPageSize: fetchSize,
		StartTimeFilter: &filterpb.StartTimeFilter{
			EarliestTime: timestamppb.New(from),
			LatestTime:   timestamppb.New(to),
		},
		Filters: &workflowservice.ListClosedWorkflowExecutionsRequest_TypeFilter{
			TypeFilter: &filterpb.WorkflowTypeFilter{Name: "SendEmailWorkflow"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list closed workflows: %w", err)
	}

	var results []*FailedNotification

	for _, exec := range resp.Executions {
		status := workflowStatus(exec.Status)
		if !statusMatches(status, filter.Status) {
			continue
		}

		provider := parseProviderFromTaskQueue(exec.TaskQueue)
		if filter.Provider != "" && provider != filter.Provider {
			continue
		}

		workflowID := exec.Execution.WorkflowId
		runID := exec.Execution.RunId

		details, detailsErr := extractWorkflowDetails(ctx, tc, workflowID, runID)
		if detailsErr != nil {
			logger.Warn("could not extract workflow details from history",
				slog.String("workflow_id", workflowID),
				slog.Any("error", detailsErr),
			)
			details = &workflowDetails{}
		}

		closedAt := exec.CloseTime.AsTime()
		lastAttemptAt := closedAt
		if !details.lastAttemptAt.IsZero() {
			lastAttemptAt = details.lastAttemptAt
		}

		results = append(results, &FailedNotification{
			WorkflowID:    workflowID,
			RunID:         runID,
			Recipient:     details.recipient,
			Subject:       details.subject,
			Provider:      provider,
			FailureReason: details.failureReason,
			RetryCount:    details.retryCount,
			LastAttemptAt: lastAttemptAt,
			ClosedAt:      closedAt,
			Status:        status,
		})
	}

	if filter.Offset >= len(results) {
		return []*FailedNotification{}, nil
	}
	results = results[filter.Offset:]
	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

type workflowDetails struct {
	msg           *models.EmailMessage // full original input; nil if not decoded
	recipient     string
	subject       string
	failureReason string
	retryCount    int32
	lastAttemptAt time.Time
}

// extractWorkflowDetails walks workflow history to collect: full input payload, failure reason, and retry count.
func extractWorkflowDetails(ctx context.Context, tc client.Client, workflowID, runID string) (*workflowDetails, error) {
	iter := tc.GetWorkflowHistory(ctx, workflowID, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	d := &workflowDetails{}

	for iter.HasNext() {
		event, err := iter.Next()
		if err != nil {
			return d, fmt.Errorf("read history event: %w", err)
		}

		switch event.EventType {
		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED:
			attrs := event.GetWorkflowExecutionStartedEventAttributes()
			if attrs != nil && attrs.Input != nil {
				var msg models.EmailMessage
				if decErr := converter.GetDefaultDataConverter().FromPayloads(attrs.Input, &msg); decErr == nil {
					d.msg = &msg
					d.recipient = msg.To
					d.subject = msg.Subject
				}
			}

		case enumspb.EVENT_TYPE_ACTIVITY_TASK_FAILED:
			d.retryCount++
			d.lastAttemptAt = event.EventTime.AsTime()
			attrs := event.GetActivityTaskFailedEventAttributes()
			if attrs != nil && attrs.Failure != nil && d.failureReason == "" {
				d.failureReason = attrs.Failure.Message
			}

		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_FAILED:
			attrs := event.GetWorkflowExecutionFailedEventAttributes()
			if attrs != nil && attrs.Failure != nil && d.failureReason == "" {
				d.failureReason = attrs.Failure.Message
			}

		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_TIMED_OUT:
			if d.failureReason == "" {
				d.failureReason = "workflow timed out"
			}

		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CANCELED:
			if d.failureReason == "" {
				d.failureReason = "workflow canceled"
			}
		}
	}

	return d, nil
}

func parseProviderFromTaskQueue(tq string) string {
	tq = strings.TrimPrefix(tq, "email-")
	tq = strings.TrimSuffix(tq, "-queue")
	return tq
}

func workflowStatus(status enumspb.WorkflowExecutionStatus) string {
	switch status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_FAILED:
		return "Failed"
	case enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return "TimedOut"
	case enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED:
		return "Canceled"
	default:
		return ""
	}
}

// statusMatches returns true when the execution status should be included given the filter.
func statusMatches(status, filterStatus string) bool {
	if filterStatus == "" {
		return status == "Failed" || status == "TimedOut" || status == "Canceled"
	}
	return status == filterStatus
}
