package api

import (
	"beacon/internal/dlq"
	"beacon/utils"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DLQHandler exposes HTTP endpoints for querying and replaying failed email workflows.
type DLQHandler struct {
	Service *dlq.DLQService
	logger  *slog.Logger
}

func NewDLQHandler(service *dlq.DLQService, logger *slog.Logger) *DLQHandler {
	return &DLQHandler{Service: service, logger: logger}
}

// HandleQueryFailures handles GET /dlq/failed.
// Query params: status, provider, from (RFC3339), to (RFC3339), limit, offset.
func (h *DLQHandler) HandleQueryFailures(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		utils.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	q := req.URL.Query()
	filter := dlq.FailureFilter{
		Status:   q.Get("status"),
		Provider: q.Get("provider"),
	}

	if raw := q.Get("from"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			filter.FromDate = t
		}
	}
	if raw := q.Get("to"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			filter.ToDate = t
		}
	}
	if raw := q.Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			filter.Limit = v
		}
	}
	if raw := q.Get("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			filter.Offset = v
		}
	}
	if filter.Limit > 100 {
		filter.Limit = 100
	}

	failures, err := h.Service.QueryFailures(req.Context(), filter)
	if err != nil {
		h.logger.Error("DLQ query failed", slog.Any("error", err))
		utils.WriteError(w, http.StatusInternalServerError, "failed to query workflow failures")
		return
	}

	utils.WriteSuccess(w, http.StatusOK, "", map[string]any{
		"failures": failures,
		"count":    len(failures),
	})
}

// HandleReplay handles POST /dlq/replay/{workflowID}.
func (h *DLQHandler) HandleReplay(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		utils.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	workflowID := strings.TrimPrefix(req.URL.Path, "/dlq/replay/")
	if workflowID == "" {
		utils.WriteError(w, http.StatusBadRequest, "workflow ID is required")
		return
	}

	result, err := h.Service.ReplayWorkflow(req.Context(), workflowID)
	if err != nil {
		if errors.Is(err, dlq.ErrWorkflowNotFound) {
			utils.WriteError(w, http.StatusNotFound, "workflow not found: "+workflowID)
			return
		}
		if errors.Is(err, dlq.ErrNotTerminalState) {
			utils.WriteError(w, http.StatusConflict, "workflow is still running; replay not allowed")
			return
		}
		h.logger.Error("DLQ replay failed", slog.String("workflow_id", workflowID), slog.Any("error", err))
		utils.WriteError(w, http.StatusInternalServerError, "replay failed")
		return
	}

	utils.WriteSuccess(w, http.StatusAccepted, "workflow replay dispatched", result)
}
