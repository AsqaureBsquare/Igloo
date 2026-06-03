package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/store"
)

// POST /api/auth/register
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Email == "" || body.Password == "" || body.Name == "" {
		h.err(w, http.StatusBadRequest, "name, email, and password are required")
		return
	}
	if len(body.Password) < 8 {
		h.err(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := h.Auth.HashPassword(body.Password)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	user := &models.User{
		ID:           uuid.NewString(),
		Email:        strings.ToLower(strings.TrimSpace(body.Email)),
		Name:         strings.TrimSpace(body.Name),
		PasswordHash: hash,
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

	token, err := h.Auth.SignUser(user.ID, user.Email)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.json(w, http.StatusCreated, map[string]any{
		"token": token,
		"user":  user,
	})
}

// POST /api/auth/login
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request")
		return
	}

	user, err := h.Store.GetUserByEmail(r.Context(), strings.ToLower(strings.TrimSpace(body.Email)))
	if err != nil {
		h.err(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	if !h.Auth.CheckPassword(user.PasswordHash, body.Password) {
		h.err(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	token, err := h.Auth.SignUser(user.ID, user.Email)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.json(w, http.StatusOK, map[string]any{
		"token": token,
		"user":  user,
	})
}

// GET /api/auth/me
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	user, err := h.Store.GetUser(r.Context(), ctxUserID(r))
	if err != nil {
		h.err(w, http.StatusNotFound, "user not found")
		return
	}
	h.json(w, http.StatusOK, user)
}

// PUT /api/auth/me/ssh-key
func (h *Handler) UpdateSSHKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SSHPublicKey string `json:"ssh_public_key"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid body")
		return
	}
	user, err := h.Store.GetUser(r.Context(), ctxUserID(r))
	if err != nil {
		h.err(w, http.StatusNotFound, "user not found")
		return
	}
	user.SSHPublicKey = strings.TrimSpace(body.SSHPublicKey)
	if err := h.Store.UpdateUser(r.Context(), user); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, user)
}

// Middleware: require valid user JWT, inject user_id into context.
func (h *Handler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			h.err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		claims, err := h.Auth.ParseUser(token)
		if err != nil {
			h.err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyUserID, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
