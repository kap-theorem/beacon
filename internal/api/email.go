package api

import (
	"beacon/internal/models"
	"beacon/internal/notifier"
	"beacon/internal/temporal"
	"beacon/utils"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"go.temporal.io/sdk/client"
)

// WorkflowStarter is the slice of the Temporal client the email handler needs.
// *client.Client (the real Temporal client) satisfies this automatically.
type WorkflowStarter interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (client.WorkflowRun, error)
}

// EmailHandler handles email notification requests.
type EmailHandler struct {
	TemporalClient WorkflowStarter
	Registry       *notifier.EmailClientRegistry
}

// HandleRequest handles "POST /notify/email".
// The optional client_hint field in the request body selects the routing category.
func (h *EmailHandler) HandleRequest(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		utils.WriteError(w, http.StatusMethodNotAllowed, "unsupported method")
		return
	}

	if h.TemporalClient == nil {
		utils.WriteError(w, http.StatusServiceUnavailable, "temporal service not available")
		return
	}

	var request models.EmailMessage
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		utils.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	trimmedTo := strings.TrimSpace(request.To)
	if trimmedTo == "" {
		utils.WriteError(w, http.StatusBadRequest, "missing required field: to")
		return
	}
	if request.Subject == "" {
		utils.WriteError(w, http.StatusBadRequest, "missing required field: subject")
		return
	}
	if _, err := mail.ParseAddress(trimmedTo); err != nil {
		utils.WriteError(w, http.StatusBadRequest, "invalid email address: to")
		return
	}

	_, providerName, err := h.Registry.Resolve(request.ClientHint)
	if err != nil {
		log.Printf("routing error for hint %q: %v", request.ClientHint, err)
		utils.WriteError(w, http.StatusBadRequest, fmt.Sprintf("routing error: %v", err))
		return
	}
	taskQueue := notifier.TaskQueueFor(providerName)

	workflowOptions := client.StartWorkflowOptions{
		ID:        fmt.Sprintf("email-workflow-%s-%d", request.To, time.Now().UnixNano()),
		TaskQueue: taskQueue,
	}

	n := &models.Notification{
		Channel:  "email",
		Provider: providerName,
		Email:    &models.EmailPayload{To: request.To, Subject: request.Subject, Body: request.Body},
	}
	we, err := h.TemporalClient.ExecuteWorkflow(req.Context(), workflowOptions, temporal.SendEmailWorkflow, n)
	if err != nil {
		log.Printf("Unable to execute workflow: %v", err)
		utils.WriteError(w, http.StatusInternalServerError, "failed to trigger email notification")
		return
	}

	log.Printf("Started workflow id=%s run=%s provider=%s", we.GetID(), we.GetRunID(), providerName)
	utils.WriteSuccess(w, http.StatusAccepted, "email notification triggered", map[string]string{
		"workflow_id":     we.GetID(),
		"workflow_run_id": we.GetRunID(),
		"provider":        providerName,
	})
}
