package dlq

import "time"

// FailedNotification represents a closed Temporal workflow execution that ended in failure.
type FailedNotification struct {
	WorkflowID    string    `json:"workflow_id"`
	RunID         string    `json:"run_id"`
	Recipient     string    `json:"recipient"`
	Subject       string    `json:"subject"`
	Provider      string    `json:"provider"`
	FailureReason string    `json:"failure_reason"`
	RetryCount    int32     `json:"retry_count"`
	LastAttemptAt time.Time `json:"last_attempt_at"`
	ClosedAt      time.Time `json:"closed_at"`
	Status        string    `json:"status"`
}

// FailureFilter controls which closed workflow executions are returned.
type FailureFilter struct {
	Status   string    // "Failed", "TimedOut", "Canceled", or "" for all three
	Provider string    // optional; match by task-queue provider name
	FromDate time.Time // inclusive start of close-time window
	ToDate   time.Time // inclusive end of close-time window
	Limit    int       // max results (default 20, max 100)
	Offset   int       // pagination offset
}

// ReplayResult is returned after successfully dispatching a new workflow execution.
type ReplayResult struct {
	NewWorkflowID      string `json:"new_workflow_id"`
	NewRunID           string `json:"new_run_id"`
	OriginalWorkflowID string `json:"original_workflow_id"`
	Provider           string `json:"provider"`
}
