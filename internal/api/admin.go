package api

import (
	"beacon/internal/auth"
	"beacon/internal/config"
	"beacon/internal/notifier"
	"beacon/utils"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

// AdminHandler exposes privileged config management endpoints.
type AdminHandler struct {
	ConfigService *config.ConfigService
	Providers     *notifier.ProviderRegistry
	AuthRegistry  *auth.Registry
	logger        *slog.Logger
}

func NewAdminHandler(cs *config.ConfigService, providers *notifier.ProviderRegistry, authReg *auth.Registry, logger *slog.Logger) *AdminHandler {
	return &AdminHandler{ConfigService: cs, Providers: providers, AuthRegistry: authReg, logger: logger}
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

	presented := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(auth.HashKey(presented)), []byte(auth.HashKey(adminToken))) != 1 {
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
	h.Providers.Reload(bundle)
	h.AuthRegistry.Reload(bundle)

	utils.WriteSuccess(w, http.StatusOK, "config refreshed", map[string]any{
		"revision":  bundle.Revision,
		"providers": h.Providers.Names("email"),
		"services":  len(bundle.Services),
	})
}
