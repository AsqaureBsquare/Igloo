package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/igloo-dev/igloo/internal/guard"
)

// The organisation-wide network baseline (see guard.Baseline) is kept in the platform
// config. It is read on every check, so a change applies to the next request and, through the
// gateway's authorize call, to workspaces that already exist.

// baseline returns the current baseline. When it cannot be read, it fails closed: saving a
// policy is refused rather than checked against nothing.
func (h *Handler) baseline(ctx context.Context) (guard.Baseline, error) {
	cfg, err := h.Store.GetPlatformConfig(ctx)
	if err != nil {
		return guard.Baseline{}, err
	}
	var b guard.Baseline
	if len(cfg.GuardBaseline) > 0 {
		if err := json.Unmarshal(cfg.GuardBaseline, &b); err != nil {
			return guard.Baseline{}, err
		}
	}
	return b, nil
}

// GET /api/guard-baseline (any signed-in user: they need it to understand a refusal)
func (h *Handler) GetGuardBaseline(w http.ResponseWriter, r *http.Request) {
	b, err := h.baseline(r.Context())
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, struct {
		guard.Baseline
		GatewayEnabled bool   `json:"gateway_enabled"`         // whether this server can run workspaces behind the gateway
		SandboxImage   string `json:"sandbox_image,omitempty"` // an image that already has what a guarded workspace needs
	}{b, h.guardEnabled(), h.SandboxImage})
}

// PUT /api/admin/guard-baseline (admin)
func (h *Handler) PutGuardBaseline(w http.ResponseWriter, r *http.Request) {
	var b guard.Baseline
	if err := h.decode(r, &b); err != nil {
		h.err(w, http.StatusBadRequest, "bad request")
		return
	}
	if err := b.Validate(); err != nil {
		h.err(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg, err := h.Store.GetPlatformConfig(r.Context())
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	raw, _ := json.Marshal(b)
	cfg.GuardBaseline = raw
	if err := h.Store.SavePlatformConfig(r.Context(), cfg); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, b)
}
