package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/igloo-dev/igloo/internal/auth"
	"github.com/igloo-dev/igloo/internal/relay"
	"github.com/igloo-dev/igloo/internal/store"
	"github.com/igloo-dev/igloo/internal/tailnet"
)

type Handler struct {
	Store       store.Store
	Auth        *auth.Auth
	Relay       *relay.Relay
	Registry    *ProvisionerRegistry // parked acquire tokens from daemon WS connections
	LogBuf      *LogBuffer           // live provisioner log lines per workspace
	Coordinator *tailnet.Coordinator // built-in WireGuard coordination (no Tailscale needed)

	// OIDC — nil when not configured
	OIDC                *auth.OIDCProvider
	DisablePasswordAuth bool     // when true, /login and /register return 403
	AllowedGroups       []string // if non-empty, user must belong to at least one of these IdP groups

	deviceMu    sync.Mutex
	deviceCodes map[string]*deviceEntry // code → entry
}

type deviceEntry struct {
	token     string    // JWT set after approval; empty while pending
	expiresAt time.Time
}

func (h *Handler) initDeviceCodes() {
	h.deviceMu.Lock()
	if h.deviceCodes == nil {
		h.deviceCodes = make(map[string]*deviceEntry)
	}
	h.deviceMu.Unlock()
}

func (h *Handler) gcDeviceCodes() {
	h.deviceMu.Lock()
	defer h.deviceMu.Unlock()
	now := time.Now()
	for code, e := range h.deviceCodes {
		if now.After(e.expiresAt) {
			delete(h.deviceCodes, code)
		}
	}
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
