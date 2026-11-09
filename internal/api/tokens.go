package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/store"
)

// POST /api/tokens
func (h *Handler) CreateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Lifetime string `json:"lifetime"` // e.g. "8760h" (1 year)
		UserID   string `json:"user_id"`  // admin-only: create token for another user
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Name == "" {
		h.err(w, http.StatusBadRequest, "name is required")
		return
	}

	callerID := ctxUserID(r)
	targetUserID := callerID

	// Admins can create tokens for other users.
	if body.UserID != "" && body.UserID != callerID {
		caller, err := h.Store.GetUser(r.Context(), callerID)
		if err != nil || !caller.IsAdmin {
			h.err(w, http.StatusForbidden, "only admins can create tokens for other users")
			return
		}
		targetUserID = body.UserID
	}

	lifetime := 365 * 24 * time.Hour // default 1 year
	if body.Lifetime != "" {
		if d, err := time.ParseDuration(body.Lifetime); err == nil && d > 0 {
			lifetime = d
		}
	}

	raw, err := generateToken()
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	t := &models.APIToken{
		ID:          uuid.NewString(),
		UserID:      targetUserID,
		TokenHash:   HashToken(raw),
		TokenPrefix: raw[:10],
		Name:        strings.TrimSpace(body.Name),
		ExpiresAt:   time.Now().Add(lifetime),
		CreatedAt:   time.Now(),
		CreatedBy:   callerID,
	}

	if err := h.Store.CreateAPIToken(r.Context(), t); err != nil {
		h.err(w, http.StatusInternalServerError, "failed to create token")
		return
	}

	h.json(w, http.StatusCreated, map[string]any{
		"id":           t.ID,
		"name":         t.Name,
		"token":        raw, // returned once only
		"token_prefix": t.TokenPrefix,
		"expires_at":   t.ExpiresAt,
		"created_at":   t.CreatedAt,
	})
}

// GET /api/tokens
func (h *Handler) ListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.Store.ListAPITokens(r.Context(), ctxUserID(r))
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if tokens == nil {
		tokens = []*models.APIToken{}
	}
	h.json(w, http.StatusOK, tokens)
}

// DELETE /api/tokens/{id}
func (h *Handler) RevokeToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Fetch to verify ownership (non-admins can only revoke their own tokens).
	tokens, err := h.Store.ListAPITokens(r.Context(), ctxUserID(r))
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	found := false
	for _, t := range tokens {
		if t.ID == id {
			found = true
			break
		}
	}
	if !found {
		// Check if caller is admin — admins can revoke any token.
		caller, err := h.Store.GetUser(r.Context(), ctxUserID(r))
		if err != nil || !caller.IsAdmin {
			h.err(w, http.StatusNotFound, "token not found")
			return
		}
	}
	if err := h.Store.DeleteAPIToken(r.Context(), id); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/admin/users — create a user. With a password it is a person who can sign in; without
// one it is a headless service account.
func (h *Handler) AdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		Email     string `json:"email"`
		Password  string `json:"password"`   // optional: makes this a user who signs in with a password
		LoginType string `json:"login_type"` // "none" for headless
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Email == "" || body.Name == "" {
		h.err(w, http.StatusBadRequest, "name and email are required")
		return
	}
	loginType := strings.ToLower(body.LoginType)
	var hash string
	if body.Password != "" {
		if loginType != "" && loginType != "password" {
			h.err(w, http.StatusBadRequest, "a password can only be set on an account that signs in with one")
			return
		}
		if err := validNewPassword(body.Password); err != nil {
			h.err(w, http.StatusBadRequest, err.Error())
			return
		}
		var err error
		if hash, err = h.Auth.HashPassword(body.Password); err != nil {
			h.err(w, http.StatusInternalServerError, "internal error")
			return
		}
		loginType = "" // the same as a user who registered themselves
	} else if loginType == "" {
		loginType = "none"
	}

	user := &models.User{
		ID:           uuid.NewString(),
		Email:        strings.ToLower(strings.TrimSpace(body.Email)),
		Name:         strings.TrimSpace(body.Name),
		PasswordHash: hash,
		LoginType:    loginType,
		CreatedAt:    time.Now(),
	}

	if err := h.Store.CreateUser(r.Context(), user); err != nil {
		if err == store.ErrConflict {
			h.err(w, http.StatusConflict, "email already registered")
			return
		}
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.json(w, http.StatusCreated, user)
}
