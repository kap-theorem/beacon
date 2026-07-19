package api

import (
	"beacon/internal/auth"
	"beacon/internal/dlq"
	"beacon/utils"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// DLQQuerier is the behavior the handler needs; *dlq.DLQService satisfies it.
type DLQQuerier interface {
	QueryFailures(ctx context.Context, filter dlq.FailureFilter) ([]*dlq.FailedNotification, error)
	ReplayWorkflow(ctx context.Context, workflowID, callerTenant string) (*dlq.ReplayResult, error)
}

// DLQHandler exposes HTTP endpoints for querying and replaying failed email workflows.
type DLQHandler struct {
	Service DLQQuerier
	logger  *slog.Logger
}

func NewDLQHandler(service DLQQuerier, logger *slog.Logger) *DLQHandler {
	return &DLQHandler{Service: service, logger: logger}
}

// HandleQueryFailures handles GET /v1/dlq/failed.
// Query params: status, provider, from (RFC3339), to (RFC3339), limit, offset, tenant (admin only).
func (h *DLQHandler) HandleQueryFailures(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	filter := dlq.FailureFilter{
		Status:   q.Get("status"),
		Provider: q.Get("provider"),
	}

	ident := auth.FromContext(req.Context())
	if ident == nil {
		utils.WriteError(w, http.StatusUnauthorized, "missing API key")
		return
	}
	if ident.Admin {
		filter.Tenant = q.Get("tenant") // optional narrowing for operators
	} else {
		filter.Tenant = ident.Tenant
	}

	if raw := q.Get("from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			utils.WriteError(w, http.StatusBadRequest, `invalid "from" date: must be RFC3339`)
			return
		}
		filter.FromDate = t
	}
	if raw := q.Get("to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			utils.WriteError(w, http.StatusBadRequest, `invalid "to" date: must be RFC3339`)
			return
		}
		filter.ToDate = t
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

// HandleReplay handles POST /v1/dlq/replay/{workflowID}.
func (h *DLQHandler) HandleReplay(w http.ResponseWriter, req *http.Request) {
	workflowID := req.PathValue("workflowID")
	if workflowID == "" {
		utils.WriteError(w, http.StatusBadRequest, "workflow ID is required")
		return
	}

	ident := auth.FromContext(req.Context())
	if ident == nil {
		utils.WriteError(w, http.StatusUnauthorized, "missing API key")
		return
	}
	callerTenant := ident.Tenant
	if ident.Admin {
		callerTenant = ""
	}

	result, err := h.Service.ReplayWorkflow(req.Context(), workflowID, callerTenant)
	if err != nil {
		if errors.Is(err, dlq.ErrWorkflowNotFound) {
			utils.WriteError(w, http.StatusNotFound, "workflow not found: "+workflowID)
			return
		}
		if errors.Is(err, dlq.ErrNotTerminalState) {
			utils.WriteError(w, http.StatusConflict, "workflow is still running; replay not allowed")
			return
		}
		if errors.Is(err, dlq.ErrReplayAlreadyRunning) {
			utils.WriteError(w, http.StatusConflict, "replay already in progress for workflow: "+workflowID)
			return
		}
		h.logger.Error("DLQ replay failed", slog.String("workflow_id", workflowID), slog.Any("error", err))
		utils.WriteError(w, http.StatusInternalServerError, "replay failed")
		return
	}

	utils.WriteSuccess(w, http.StatusAccepted, "workflow replay dispatched", result)
}
