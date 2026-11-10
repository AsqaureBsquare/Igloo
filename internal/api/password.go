package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/igloo-dev/igloo/internal/auth"
)

// Changing a password also ends every session that was open before it: a session token carries its
// issue time, and one issued before the user's PasswordChangedAt is refused.

const (
	minPasswordLen = 8
	maxPasswordLen = 72 // bcrypt reads no more than 72 bytes
)

func validNewPassword(pw string) error {
	switch {
	case len(pw) < minPasswordLen:
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	case len(pw) > maxPasswordLen:
		return fmt.Errorf("password must be at most %d characters", maxPasswordLen)
	}
	return nil
}

// sessionRevoked reports whether the user changed their password after this token was issued. A
// user the store cannot find is not treated as revoked here, so the rest of the request decides.
func (h *Handler) sessionRevoked(ctx context.Context, c *auth.UserClaims) bool {
	u, err := h.Store.GetUser(ctx, c.UserID)
	if err != nil || u.PasswordChangedAt.IsZero() {
		return false
	}
	if c.IssuedAt == nil {
		return true
	}
	return c.IssuedAt.Time.Before(u.PasswordChangedAt.Truncate(time.Second)) // tokens are stamped to the second
}

// POST /api/auth/password {"current_password": "...", "new_password": "..."}
// Changes the caller's own password and returns a fresh session token; every older one stops working.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	if h.DisablePasswordAuth {
		h.err(w, http.StatusForbidden, "password auth disabled — use SSO")
		return
	}
	var body struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request")
		return
	}
	user, err := h.Store.GetUser(r.Context(), ctxUserID(r))
	if err != nil {
		h.err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if user.LoginType == "oidc" || user.LoginType == "none" || user.PasswordHash == "" {
		h.err(w, http.StatusForbidden, "this account has no password; it signs in another way")
		return
	}
	limitKey := "pw|" + user.ID + "|" + clientIP(r) // a stolen session must not be a way to guess the password
	if h.loginRL.blocked(limitKey) {
		w.Header().Set("Retry-After", "600")
		h.err(w, http.StatusTooManyRequests, "too many failed attempts — try again in a few minutes")
		return
	}
	if !h.Auth.CheckPassword(user.PasswordHash, body.Current) {
		h.loginRL.fail(limitKey)
		h.err(w, http.StatusForbidden, "current password is incorrect")
		return
	}
	h.loginRL.reset(limitKey)
	if err := validNewPassword(body.New); err != nil {
		h.err(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.New == body.Current {
		h.err(w, http.StatusBadRequest, "choose a password you are not already using")
		return
	}
	if !h.setPassword(w, r, user.ID, body.New) {
		return
	}
	token, err := h.Auth.SignUser(user.ID, user.Email)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, map[string]any{"token": token})
}

// PUT /api/admin/users/{id}/password {"new_password": "..."} (admin)
// Sets another user's password, for one who has forgotten theirs. Their open sessions end.
func (h *Handler) AdminResetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		New string `json:"new_password"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request")
		return
	}
	user, err := h.Store.GetUser(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.err(w, http.StatusNotFound, "user not found")
		return
	}
	if user.LoginType == "oidc" || user.LoginType == "none" {
		h.err(w, http.StatusConflict, "this account has no password; it signs in another way")
		return
	}
	if err := validNewPassword(body.New); err != nil {
		h.err(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.setPassword(w, r, user.ID, body.New) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setPassword stores the new hash and the time of the change; on failure it has already answered.
func (h *Handler) setPassword(w http.ResponseWriter, r *http.Request, userID, password string) bool {
	hash, err := h.Auth.HashPassword(password)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return false
	}
	user, err := h.Store.GetUser(r.Context(), userID)
	if err != nil {
		h.err(w, http.StatusNotFound, "user not found")
		return false
	}
	user.PasswordHash = hash
	user.PasswordChangedAt = time.Now()
	if err := h.Store.UpdateUser(r.Context(), user); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return false
	}
	return true
}
