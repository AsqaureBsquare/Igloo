package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
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
