package api

import (
	"beacon/internal/auth"
	"beacon/internal/config"
	"beacon/internal/notifier"
	"beacon/utils"
	"errors"
	"log/slog"
	"net/http"
	"os"
)

// AdminHandler exposes privileged config management endpoints.
type AdminHandler struct {
	ConfigService  *config.ConfigService
	Providers      *notifier.ProviderRegistry
	AuthRegistry   *auth.Registry
	LegacyRegistry *notifier.EmailClientRegistry // removed at cutover
	logger         *slog.Logger
}

func NewAdminHandler(cs *config.ConfigService, providers *notifier.ProviderRegistry, authReg *auth.Registry, legacy *notifier.EmailClientRegistry, logger *slog.Logger) *AdminHandler {
	return &AdminHandler{ConfigService: cs, Providers: providers, AuthRegistry: authReg, LegacyRegistry: legacy, logger: logger}
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
		if errors.Is(err, config.ErrDevModeSkip) {
			utils.WriteError(w, http.StatusServiceUnavailable, "config refresh is not available in DEV_MODE")
			return
		}
		h.logger.Error("admin config refresh failed", slog.Any("error", err))
		utils.WriteError(w, http.StatusInternalServerError, "config refresh failed")
		return
	}

	bundle := h.ConfigService.GetConfig()
	// Reload order: provider/auth registries first, legacy last; a legacy
	// failure (empty SMTP) leaves registries momentarily divergent until the
	// next successful poll — acceptable until legacy is removed (Task 12).
	h.Providers.Reload(bundle)
	h.AuthRegistry.Reload(bundle)
	if h.LegacyRegistry != nil {
		if err := h.LegacyRegistry.Reload(bundle); err != nil {
			h.logger.Error("legacy registry reload after admin refresh failed", slog.Any("error", err))
			utils.WriteError(w, http.StatusInternalServerError, "registry reload failed")
			return
		}
	}

	utils.WriteSuccess(w, http.StatusOK, "config refreshed", map[string]any{
		"revision":  bundle.Revision,
		"providers": h.Providers.Names("email"),
		"services":  len(bundle.Services),
	})
}
