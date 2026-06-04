package api

import (
	"beacon/internal/models"
	"beacon/internal/notifier"
	"beacon/internal/temporal"
	"beacon/utils"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"go.temporal.io/sdk/client"
)

// EmailHandler handles email notification requests.
type EmailHandler struct {
	TemporalClient client.Client
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
	if request.To == "" || request.Subject == "" {
		utils.WriteError(w, http.StatusBadRequest, "missing required fields: to, subject")
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

	we, err := h.TemporalClient.ExecuteWorkflow(req.Context(), workflowOptions, temporal.SendEmailWorkflow, &request)
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
