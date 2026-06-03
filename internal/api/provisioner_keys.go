package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/igloo-dev/igloo/internal/models"
)

// CreateProvisionerKey handles POST /api/admin/provisioner-keys.
// Returns the raw token once — it is never stored in plain text.
func (h *Handler) CreateProvisionerKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string            `json:"name"`
		Tags map[string]string `json:"tags"`
	}
	if err := h.decode(r, &req); err != nil || req.Name == "" {
		h.err(w, http.StatusBadRequest, "name required")
		return
	}

	raw, err := generateToken()
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	hash := hashToken(raw)

	k := &models.ProvisionerKey{
		ID:        uuid.NewString(),
		Name:      req.Name,
		Tags:      req.Tags,
		TokenHash: hash,
		CreatedBy: ctxUserID(r),
		CreatedAt: time.Now(),
	}
	if err := h.Store.CreateProvisionerKey(r.Context(), k); err != nil {
		h.err(w, http.StatusInternalServerError, "failed to create key")
		return
	}

	h.json(w, http.StatusCreated, map[string]any{
		"id":         k.ID,
		"name":       k.Name,
		"tags":       k.Tags,
		"created_at": k.CreatedAt,
		"token":      raw, // shown once
	})
}

// ListProvisionerKeys handles GET /api/admin/provisioner-keys.
func (h *Handler) ListProvisionerKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.Store.ListProvisionerKeys(r.Context())
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to list keys")
		return
	}
	if keys == nil {
		keys = []*models.ProvisionerKey{}
	}
	h.json(w, http.StatusOK, keys)
}

// DeleteProvisionerKey handles DELETE /api/admin/provisioner-keys/{id}.
func (h *Handler) DeleteProvisionerKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Store.DeleteProvisionerKey(r.Context(), id); err != nil {
		h.err(w, http.StatusNotFound, "key not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(token string) string { return HashToken(token) }

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
