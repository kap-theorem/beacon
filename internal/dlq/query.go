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

// maxListPages bounds the number of ListClosedWorkflow RPCs issued per DLQ
// query, so a tenant filter that discards most of a page cannot page forever.
const maxListPages = 10

// QueryFailures lists closed SendEmailWorkflow executions and applies in-process filtering (S4, S6).
// Uses ListClosedWorkflow (no Elasticsearch required) with TypeFilter, then filters by status/provider/date.
func (s *DLQService) QueryFailures(ctx context.Context, filter FailureFilter) ([]*FailedNotification, error) {
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
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	// Fetch more than needed so in-process filters still honour the requested limit.
	fetchSize := int32(limit+filter.Offset) * 3
	if fetchSize < 50 {
		fetchSize = 50
	}

	// Collect matching executions first (lightweight — no history RPCs), page
	// them, then fetch workflow history only for the executions being returned.
	type matchedExec struct {
		workflowID string
		runID      string
		provider   string
		service    string
		tenant     string
		status     string
		closedAt   time.Time
	}

	var matches []matchedExec
	var nextPageToken []byte

	for page := 0; page < maxListPages; page++ {
		resp, err := s.tc.ListClosedWorkflow(ctx, &workflowservice.ListClosedWorkflowExecutionsRequest{
			Namespace:       s.namespace,
			MaximumPageSize: fetchSize,
			NextPageToken:   nextPageToken,
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

		for _, exec := range resp.Executions {
			status := workflowStatus(exec.Status)
			if !statusMatches(status, filter.Status) {
				continue
			}

			tenant := memoString(exec.Memo, "tenant")
			if filter.Tenant != "" && tenant != filter.Tenant {
				continue
			}
			service := memoString(exec.Memo, "service")

			provider := memoString(exec.Memo, "provider")
			if provider == "" {
				provider = parseProviderFromTaskQueue(exec.TaskQueue)
			}
			if filter.Provider != "" && provider != filter.Provider {
				continue
			}

			matches = append(matches, matchedExec{
				workflowID: exec.Execution.WorkflowId,
				runID:      exec.Execution.RunId,
				provider:   provider,
				service:    service,
				tenant:     tenant,
				status:     status,
				closedAt:   exec.CloseTime.AsTime(),
			})
		}

		nextPageToken = resp.NextPageToken
		// Stop once we have enough post-filter matches to serve the requested
		// page, or there are no more pages to fetch.
		if len(matches) >= limit+filter.Offset || len(nextPageToken) == 0 {
			break
		}
	}

	if filter.Offset >= len(matches) {
		return []*FailedNotification{}, nil
	}
	matches = matches[filter.Offset:]
	if len(matches) > limit {
		matches = matches[:limit]
	}

	results := make([]*FailedNotification, 0, len(matches))

	for _, m := range matches {
		details, detailsErr := extractWorkflowDetails(ctx, s.tc, m.workflowID, m.runID)
		if detailsErr != nil {
			s.logger.Warn("could not extract workflow details from history",
				slog.String("workflow_id", m.workflowID),
				slog.Any("error", detailsErr),
			)
			details = &workflowDetails{}
		}

		lastAttemptAt := m.closedAt
		if !details.lastAttemptAt.IsZero() {
			lastAttemptAt = details.lastAttemptAt
		}

		results = append(results, &FailedNotification{
			WorkflowID:    m.workflowID,
			RunID:         m.runID,
			Recipient:     details.recipient,
			Subject:       details.subject,
			Provider:      m.provider,
			Service:       m.service,
			Tenant:        m.tenant,
			FailureReason: details.failureReason,
			RetryCount:    details.retryCount,
			LastAttemptAt: lastAttemptAt,
			ClosedAt:      m.closedAt,
			Status:        m.status,
		})
	}

	return results, nil
}

type workflowDetails struct {
	msg           *models.Notification // full original input (envelope form); nil if not decoded
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
				var n models.Notification
				if decErr := converter.GetDefaultDataConverter().FromPayloads(attrs.Input, &n); decErr == nil {
					// Workflow input may be either the envelope shape (new) or the
					// legacy EmailMessage shape recorded by pre-cutover workflows;
					// Normalize upgrades the latter so both decode to n.Email.
					n.Normalize()
					d.msg = &n
					if n.Email != nil {
						d.recipient = n.Email.To
						d.subject = n.Email.Subject
					}
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
	tq = strings.TrimSuffix(tq, "-queue")
	if i := strings.Index(tq, "-"); i >= 0 {
		return tq[i+1:]
	}
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
	return strings.EqualFold(status, filterStatus)
}
