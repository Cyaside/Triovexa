package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Cyaside/Triovexa/internal/coderepair"
	"github.com/Cyaside/Triovexa/internal/coderepair/sandbox"
	"github.com/Cyaside/Triovexa/internal/domain"
)

type bindingAdminStore interface {
	ListRepositoryBindings(context.Context) ([]coderepair.RepositoryBinding, error)
	DisableRepositoryBinding(context.Context, string) (bool, error)
	GetRepositoryBinding(context.Context, string) (coderepair.RepositoryBinding, error)
}

func registerBindingAdminAPI(mux *http.ServeMux, store bindingAdminStore) {
	mux.HandleFunc("/api/v1/repair/repositories", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := currentIdentity(r.Context())
		if !ok || identity.User.Role != domain.RoleAdmin {
			writeAPIError(w, http.StatusForbidden, "admin_required", "Administrator role is required.")
			return
		}
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
			return
		}
		items, err := store.ListRepositoryBindings(r.Context())
		if err != nil {
			writeAPIError(w, 500, "storage_error", "Repository configurations could not be loaded.")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})
	mux.HandleFunc("/api/v1/repair/repositories/", func(w http.ResponseWriter, r *http.Request) {
		identity, ok := currentIdentity(r.Context())
		if !ok || identity.User.Role != domain.RoleAdmin {
			writeAPIError(w, http.StatusForbidden, "admin_required", "Administrator role is required.")
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/repair/repositories/"), "/")
		if len(parts) != 2 || r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
			return
		}
		switch parts[1] {
		case "disable":
			ok, err := store.DisableRepositoryBinding(r.Context(), parts[0])
			if err != nil || !ok {
				writeAPIError(w, 409, "binding_conflict", "Repository binding is unavailable or already disabled.")
				return
			}
			writeJSON(w, 200, map[string]bool{"disabled": true})
		case "check":
			binding, err := store.GetRepositoryBinding(r.Context(), parts[0])
			if err != nil || !binding.Enabled {
				writeAPIError(w, 404, "not_found", "Active repository binding was not found.")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			sha, err := sandbox.ResolveBaseRevision(ctx, binding)
			if err != nil {
				writeAPIError(w, 422, "repository_unavailable", "Cannot read the configured GitHub branch. Check the repository URL, branch and server credential reference.")
				return
			}
			writeJSON(w, 200, map[string]string{"status": "connected", "base_sha": sha})
		default:
			writeAPIError(w, 404, "not_found", "Repository action was not found.")
		}
	})
}
