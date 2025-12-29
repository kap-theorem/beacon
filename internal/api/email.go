package api

import (
	"beacon/internal/models"
	"beacon/internal/temporal"
	"beacon/utils"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"go.temporal.io/sdk/client"
)

// EmailHandler handles email notification requests
type EmailHandler struct {
	TemporalClient         client.Client
	EmailNotifierTaskQueue string
}

// HandleEmailNotification handles "POST /notify/email"
func (h *EmailHandler) HandleRequest(w http.ResponseWriter, req *http.Request) {
	// validate request method
	if req.Method != http.MethodPost {
		utils.WriteError(w, http.StatusMethodNotAllowed, "unsupported method")
		return
	}
	// parse request body
	var request models.EmailMessage
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		utils.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if request.To == "" || request.Subject == "" {
		utils.WriteError(w, http.StatusBadRequest, "missing required fields: to, subject")
		return
	}
	// set workflow options
	workflowOptions := client.StartWorkflowOptions{
		ID:        fmt.Sprintf("email-workflow-%s-%d", request.To, time.Now().UnixNano()),
		TaskQueue: h.EmailNotifierTaskQueue,
	}
	// execute workflow
	we, err := h.TemporalClient.ExecuteWorkflow(req.Context(), workflowOptions, temporal.SendEmailWorkflow, &request)
	if err != nil {
		log.Printf("Unable to execute workflow: %v", err)
		utils.WriteError(w, http.StatusInternalServerError, "failed to trigger email notification")
		return
	}
	// respond with success
	log.Printf("Started workflow with ID: %s, RunID: %s\n", we.GetID(), we.GetRunID())
	utils.WriteSuccess(w, http.StatusAccepted, "email notification triggered", map[string]string{
		"workflow_id":     we.GetID(),
		"workflow_run_id": we.GetRunID(),
	})
}
