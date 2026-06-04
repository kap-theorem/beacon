package api

import (
	"beacon/internal/config"
	"beacon/internal/notifier"
	"beacon/utils"
	"log/slog"
	"net/http"
	"os"
)

// AdminHandler exposes privileged config management endpoints.
type AdminHandler struct {
	ConfigService *config.ConfigService
	Registry      *notifier.EmailClientRegistry
	logger        *slog.Logger
}

func NewAdminHandler(cs *config.ConfigService, registry *notifier.EmailClientRegistry, logger *slog.Logger) *AdminHandler {
	return &AdminHandler{ConfigService: cs, Registry: registry, logger: logger}
}

// HandleConfigRefresh handles POST /admin/config/refresh.
// Protected by ADMIN_TOKEN env var; returns 403 when the var is unset (endpoint disabled).
func (h *AdminHandler) HandleConfigRefresh(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		utils.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	adminToken := os.Getenv("ADMIN_TOKEN")
	if adminToken == "" {
		utils.WriteError(w, http.StatusForbidden, "admin endpoint disabled")
		return
	}

	if req.Header.Get("Authorization") != "Bearer "+adminToken {
		utils.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if err := h.ConfigService.RefreshConfig(req.Context()); err != nil {
		h.logger.Error("admin config refresh failed", slog.Any("error", err))
		utils.WriteError(w, http.StatusInternalServerError, "config refresh failed")
		return
	}

	bundle := h.ConfigService.GetConfig()
	if err := h.Registry.Reload(bundle); err != nil {
		h.logger.Error("registry reload after admin refresh failed", slog.Any("error", err))
		utils.WriteError(w, http.StatusInternalServerError, "registry reload failed")
		return
	}

	utils.WriteSuccess(w, http.StatusOK, "config refreshed", map[string]any{
		"revision":  bundle.Revision,
		"providers": h.Registry.ProviderNames(),
	})
}
