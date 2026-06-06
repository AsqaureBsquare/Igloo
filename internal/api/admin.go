package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/igloo-dev/igloo/internal/models"
)

// RequireAdmin middleware — 403 if the user is not an admin.
func (h *Handler) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := h.Store.GetUser(r.Context(), ctxUserID(r))
		if err != nil || !user.IsAdmin {
			h.err(w, http.StatusForbidden, "admin access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// GET /api/admin/users
func (h *Handler) AdminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.Store.ListUsers(r.Context())
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, users)
}

// DELETE /api/admin/users/{id}
func (h *Handler) AdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == ctxUserID(r) {
		h.err(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}
	if err := h.Store.DeleteUser(r.Context(), id); err != nil {
		h.err(w, http.StatusNotFound, "user not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/admin/platform-config
// Returns platform config. The actual API key is masked — only configured/not is exposed.
func (h *Handler) GetPlatformConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.Store.GetPlatformConfig(r.Context())
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, map[string]any{
		"tailscale_configured": cfg.TailscaleAPIKey != "",
		"tailscale_tailnet":    cfg.TailscaleTailnet,
	})
}

// PUT /api/admin/platform-config
func (h *Handler) SavePlatformConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TailscaleAPIKey  string `json:"tailscale_api_key"`
		TailscaleTailnet string `json:"tailscale_tailnet"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid body")
		return
	}
	cfg, err := h.Store.GetPlatformConfig(r.Context())
	if err != nil {
		cfg = &models.PlatformConfig{}
	}
	// Empty string means "clear the key"; anything else updates it.
	cfg.TailscaleAPIKey = body.TailscaleAPIKey
	if body.TailscaleTailnet != "" {
		cfg.TailscaleTailnet = body.TailscaleTailnet
	}
	if err := h.Store.SavePlatformConfig(r.Context(), cfg); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, map[string]any{
		"tailscale_configured": cfg.TailscaleAPIKey != "",
		"tailscale_tailnet":    cfg.TailscaleTailnet,
	})
}

// PUT /api/admin/users/{id} — update admin flag
func (h *Handler) AdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	user, err := h.Store.GetUser(r.Context(), id)
	if err != nil {
		h.err(w, http.StatusNotFound, "user not found")
		return
	}

	var body struct {
		IsAdmin *bool `json:"is_admin"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.IsAdmin != nil {
		user.IsAdmin = *body.IsAdmin
	}
	if err := h.Store.UpdateUser(r.Context(), user); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, user)
}

// GET /api/admin/workspaces?q=owner:alice+status:running+template:kubernetes
// Returns all workspaces across all users and orgs, with optional search filters.
func (h *Handler) AdminListWorkspaces(w http.ResponseWriter, r *http.Request) {
	f := ParseWorkspaceFilter(r.URL.Query().Get("q"))

	// Fetch all workspaces — empty userID and orgID = no owner/org restriction.
	ws, err := h.Store.ListWorkspaces(r.Context(), "", "")
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	tplNames := h.templateNameIndex(r.Context())
	ws = applyWorkspaceFilter(ws, f, tplNames)

	if ws == nil {
		ws = []*models.Workspace{}
	}
	h.json(w, http.StatusOK, ws)
}

// canAccessWorkspace returns true if the caller owns the workspace or is a site admin.
func (h *Handler) canAccessWorkspace(r *http.Request, ws *models.Workspace) bool {
	callerID := ctxUserID(r)
	if ws.UserID == callerID {
		return true
	}
	user, err := h.Store.GetUser(r.Context(), callerID)
	return err == nil && user.IsAdmin
}

// templateNameIndex builds a map of template ID → template name for search filtering.
func (h *Handler) templateNameIndex(ctx context.Context) map[string]string {
	templates, err := h.Store.ListTemplates(ctx, "")
	if err != nil {
		return map[string]string{}
	}
	m := make(map[string]string, len(templates))
	for _, t := range templates {
		m[t.ID] = t.Name
	}
	return m
}
