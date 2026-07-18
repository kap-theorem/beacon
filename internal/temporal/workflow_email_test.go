package temporal

import (
	"beacon/internal/models"
	"beacon/internal/notifier"
	"errors"
	"testing"

	"go.temporal.io/sdk/testsuite"
)

// TestSendEmailWorkflow_Success verifies that SendEmailWorkflow completes
// without error when the underlying notifier succeeds.
func TestSendEmailWorkflow_Success(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	fn := &fakeNotifier{}
	a := &EmailActivities{GetService: func() notifier.Notifier[models.EmailMessage] { return fn }}
	env.RegisterActivity(a.SendEmailActivity)

	env.ExecuteWorkflow(SendEmailWorkflow, &models.EmailMessage{To: "a@b.com", Subject: "s"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("unexpected workflow error: %v", err)
	}
}

// TestSendEmailWorkflow_RetriesThenFails verifies that SendEmailWorkflow
// propagates a failure once all retry attempts have been exhausted.
// The test SDK uses a simulated clock so retries complete instantly.
func TestSendEmailWorkflow_RetriesThenFails(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()

	fn := &fakeNotifier{err: errors.New("smtp down")}
	a := &EmailActivities{GetService: func() notifier.Notifier[models.EmailMessage] { return fn }}
	env.RegisterActivity(a.SendEmailActivity)

	env.ExecuteWorkflow(SendEmailWorkflow, &models.EmailMessage{To: "a@b.com", Subject: "s"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected workflow error after retries exhausted, got nil")
	}
}
