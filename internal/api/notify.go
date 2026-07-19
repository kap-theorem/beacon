package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"beacon/internal/auth"
	"beacon/internal/channel"
	"beacon/internal/notifier"
	"beacon/internal/policy"
	"beacon/utils"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	temporalerr "go.temporal.io/sdk/temporal"
)

const maxBodyBytes = 256 << 10 // 256 KB

var idempotencyKeyRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// WorkflowStarter is the slice of the Temporal client the notify handler
// needs. *client.Client (the real Temporal client) satisfies this
// automatically.
type WorkflowStarter interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (client.WorkflowRun, error)
}

// NotifyHandler serves POST /v1/notify/{channel}: policy enforcement and
// enqueue. Authentication happens in auth.Middleware before this handler.
type NotifyHandler struct {
	TemporalClient WorkflowStarter
	Channels       channel.Registry
	Providers      *notifier.ProviderRegistry
	Limiter        policy.RateLimiter
	Logger         *slog.Logger
	Now            func() time.Time // test seam; nil = time.Now
}

func (h *NotifyHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *NotifyHandler) Handle(w http.ResponseWriter, r *http.Request) {
	ident := auth.FromContext(r.Context())
	if ident == nil {
		utils.WriteError(w, http.StatusUnauthorized, "missing API key")
		return
	}
	if ident.Admin {
		utils.WriteError(w, http.StatusForbidden, "admin token cannot send notifications")
		return
	}
	if h.TemporalClient == nil {
		utils.WriteError(w, http.StatusServiceUnavailable, "temporal service not available")
		return
	}

	ch, ok := h.Channels[r.PathValue("channel")]
	if !ok {
		utils.WriteError(w, http.StatusNotFound, "unknown channel: "+r.PathValue("channel"))
		return
	}

	pol, ok := ident.Channels[ch.Name()]
	if !ok {
		utils.WriteError(w, http.StatusForbidden,
			fmt.Sprintf("channel %q not enabled for service %q", ch.Name(), ident.Service))
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			utils.WriteError(w, http.StatusRequestEntityTooLarge, "request body exceeds 256 KB")
			return
		}
		utils.WriteError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	creq, err := ch.DecodeRequest(body)
	if err != nil {
		utils.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	provider, err := policy.ResolveProvider(pol, creq.Provider)
	if err != nil {
		utils.WriteError(w, http.StatusForbidden, err.Error())
		return
	}
	if !h.Providers.Exists(ch.Name(), provider) {
		utils.WriteError(w, http.StatusServiceUnavailable,
			fmt.Sprintf("provider %q is not configured", provider))
		return
	}

	idem := r.Header.Get("Idempotency-Key")
	if idem != "" && !idempotencyKeyRe.MatchString(idem) {
		utils.WriteError(w, http.StatusBadRequest, "invalid Idempotency-Key: 1-128 chars of A-Za-z0-9._-")
		return
	}

	if ok, retryAfter := h.Limiter.Allow(ident.Service, ch.Name(), pol.Rate); !ok {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retryAfter.Seconds())+1))
		utils.WriteError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	n := creq.Notification
	n.Service = ident.Service
	n.Tenant = ident.Tenant
	n.Provider = provider
	n.CreatedAt = h.now().UTC()
	if pol.From != nil && n.Email != nil {
		n.Email.FromAddress = pol.From.Address
		n.Email.FromName = pol.From.Name
	}

	opts := client.StartWorkflowOptions{
		TaskQueue: ch.TaskQueue(provider),
		Memo: map[string]interface{}{
			"service": ident.Service, "tenant": ident.Tenant,
			"channel": ch.Name(), "provider": provider,
		},
	}
	if idem != "" {
		opts.ID = fmt.Sprintf("%s-%s-%s", ch.Name(), ident.Service, idem)
		opts.WorkflowIDReusePolicy = enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE
	} else {
		opts.ID = fmt.Sprintf("%s-%s-%d", ch.Name(), ident.Service, h.now().UnixNano())
	}

	we, err := h.TemporalClient.ExecuteWorkflow(r.Context(), opts, ch.WorkflowName(), n)
	if err != nil {
		if temporalerr.IsWorkflowExecutionAlreadyStartedError(err) {
			// A duplicate still consumed a rate token: refunding would need
			// post-hoc limiter APIs for a rare, cheap case — deliberate trade-off.
			utils.WriteSuccess(w, http.StatusAccepted, "duplicate request: notification already accepted", map[string]any{
				"workflow_id": opts.ID, "workflow_run_id": "", "provider": provider, "duplicate": true,
			})
			return
		}
		h.Logger.Error("failed to start workflow",
			slog.String("service", ident.Service), slog.Any("error", err))
		utils.WriteError(w, http.StatusInternalServerError, "failed to trigger notification")
		return
	}

	h.Logger.Info("notification accepted",
		slog.String("service", ident.Service), slog.String("tenant", ident.Tenant),
		slog.String("channel", ch.Name()), slog.String("provider", provider),
		slog.String("workflow_id", we.GetID()))
	utils.WriteSuccess(w, http.StatusAccepted, "notification accepted", map[string]any{
		"workflow_id": we.GetID(), "workflow_run_id": we.GetRunID(),
		"provider": provider, "duplicate": false,
	})
}
