package api

import (
	"encoding/json"
	"net/http"

	"github.com/igloo-dev/igloo/internal/auth"
	"github.com/igloo-dev/igloo/internal/relay"
	"github.com/igloo-dev/igloo/internal/store"
)

type Handler struct {
	Store          store.Store
	Auth           *auth.Auth
	Relay          *relay.Relay
	ProvisionerPSK string               // if set, provisioner API is enabled
	Registry       *ProvisionerRegistry // parked acquire tokens from daemon WS connections
}

func (h *Handler) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (h *Handler) err(w http.ResponseWriter, status int, msg string) {
	h.json(w, status, map[string]string{"error": msg})
}

func (h *Handler) decode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// ctxUserID extracts the user ID set by the auth middleware.
func ctxUserID(r *http.Request) string {
	v, _ := r.Context().Value(ctxKeyUserID).(string)
	return v
}

type ctxKey string

const ctxKeyUserID ctxKey = "user_id"
