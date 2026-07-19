package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"beacon/utils"
)

type ctxKey struct{}

// FromContext returns the authenticated identity, or nil.
func FromContext(ctx context.Context) *Identity {
	ident, _ := ctx.Value(ctxKey{}).(*Identity)
	return ident
}

// Middleware authenticates requests via Authorization: Bearer or X-API-Key.
// A token equal to ADMIN_TOKEN yields an unscoped admin identity.
func Middleware(reg *Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				utils.WriteError(w, http.StatusUnauthorized, "missing API key")
				return
			}
			if admin := os.Getenv("ADMIN_TOKEN"); admin != "" &&
				subtle.ConstantTimeCompare([]byte(token), []byte(admin)) == 1 {
				ctx := context.WithValue(r.Context(), ctxKey{}, &Identity{Service: "admin", Admin: true, Enabled: true})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			ident, ok := reg.Authenticate(token)
			if !ok {
				utils.WriteError(w, http.StatusUnauthorized, "invalid API key")
				return
			}
			if !ident.Enabled {
				utils.WriteError(w, http.StatusForbidden, "service disabled")
				return
			}
			ctx := context.WithValue(r.Context(), ctxKey{}, ident)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.Header.Get("X-API-Key")
}
