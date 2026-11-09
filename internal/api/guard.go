package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/igloo-dev/igloo/internal/guard"
	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/store"
)

// The egress gateway ("igloo-guard") keeps a workspace's network access to what its owner
// declared and records what it did. This file is the server's side of that:
//
//   - a workspace's network policy is stored with it (Meta["guard_policy"]), so it is never
//     mixed with secrets: the policy only names secrets the gateway holds;
//   - the provisioner is handed the gateway settings at claim time, built here, so a user
//     cannot opt out by setting their own variables;
//   - the gateway asks the server which workspace a token belongs to and what it may reach,
//     and sends back an audit trail the owner can read.

const (
	metaGuardPolicy  = "guard_policy"
	guardTokenRole   = "egress"
	guardTokenTTL    = 30 * 24 * time.Hour
	maxGuardEvents   = 1000
	maxGuardField    = 512
	maxActivityLimit = 1000
)

// policyOf returns a workspace's network policy; ok is false when it is not guarded.
func policyOf(ws *models.Workspace) (guard.Policy, bool) {
	raw := ws.Meta[metaGuardPolicy]
	if raw == "" {
		return guard.Policy{}, false
	}
	var p guard.Policy
	if json.Unmarshal([]byte(raw), &p) != nil {
		return guard.Policy{}, true // present but unreadable: treat as guarded with nothing allowed
	}
	return p, true
}

func setPolicy(ws *models.Workspace, p guard.Policy) {
	if ws.Meta == nil {
		ws.Meta = map[string]string{}
	}
	b, _ := json.Marshal(p)
	ws.Meta[metaGuardPolicy] = string(b)
}

// guardEnabled reports whether this server is set up to run workspaces behind a gateway.
func (h *Handler) guardEnabled() bool { return h.GuardKey != "" && h.GuardProxyURL != "" }

// claimVars returns the Terraform variables for a workspace. Every variable starting with
// "guard_" that a user supplied is dropped and replaced with what the server decides, so a
// workspace cannot talk its way out of the gateway. A guarded workspace also gets
// guard_required, which its template checks, so it fails to start instead of running
// unguarded if the gateway is not configured.
func (h *Handler) claimVars(ws *models.Workspace) map[string]string {
	out := make(map[string]string, len(ws.Vars)+4)
	for k, v := range ws.Vars {
		if !strings.HasPrefix(k, "guard_") {
			out[k] = v
		}
	}
	if _, guarded := policyOf(ws); !guarded {
		return out
	}
	out["guard_required"] = "true"
	if !h.guardEnabled() {
		return out
	}
	base, err := url.Parse(h.GuardProxyURL)
	if err != nil {
		return out
	}
	tok, err := h.Auth.SignConn(ws.ID, guardTokenRole, guardTokenTTL)
	if err != nil {
		return out
	}
	proxy := *base
	proxy.User = url.UserPassword("igloo", tok)
	out["guard_proxy"] = proxy.String()
	out["guard_route_base"] = strings.TrimSuffix(base.String(), "/") + "/route"
	out["guard_token"] = tok
	// http://igloo-guard.<namespace>.svc.cluster.local:3128 -> <namespace>
	if labels := strings.Split(base.Hostname(), "."); len(labels) >= 3 && labels[2] == "svc" {
		out["guard_namespace"] = labels[1]
	}
	return out
}

// GuardAuth lets only the gateway (holding the guard key) through.
func (h *Handler) GuardAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.GuardKey == "" {
			h.err(w, http.StatusServiceUnavailable, "the egress gateway is not enabled on this server")
			return
		}
		got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(h.GuardKey)) != 1 {
			h.err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// POST /api/guard/authorize {"token": "..."} (gateway only)
// Answers who the token belongs to and what that workspace may do.
func (h *Handler) GuardAuthorize(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := h.decode(r, &body); err != nil || body.Token == "" {
		h.err(w, http.StatusBadRequest, "token required")
		return
	}
	claims, err := h.Auth.ParseConn(body.Token)
	if err != nil || claims.Role != guardTokenRole {
		h.err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ws, err := h.Store.GetWorkspace(r.Context(), claims.WorkspaceID)
	if err != nil || ws.Status == models.StatusDeleting || ws.Status == models.StatusDeleted {
		h.err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	pol, guarded := policyOf(ws)
	if !guarded {
		h.err(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	base, err := h.baseline(r.Context())
	if err != nil {
		h.err(w, http.StatusServiceUnavailable, "unavailable") // fail closed: the gateway refuses
		return
	}
	h.json(w, http.StatusOK, guard.Identity{WorkspaceID: ws.ID, Policy: base.Apply(pol)})
}

// POST /api/guard/events {"events": [...]} (gateway only)
func (h *Handler) GuardEvents(w http.ResponseWriter, r *http.Request) {
	es, ok := h.Store.(store.GuardEventStore)
	if !ok {
		h.err(w, http.StatusNotImplemented, "this server's database keeps no network activity")
		return
	}
	var body struct {
		Events []models.GuardEvent `json:"events"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "bad request")
		return
	}
	if len(body.Events) > maxGuardEvents {
		h.err(w, http.StatusRequestEntityTooLarge, "too many events in one batch")
		return
	}
	known := map[string]bool{}
	keep := make([]models.GuardEvent, 0, len(body.Events))
	for _, e := range body.Events {
		ok, seen := known[e.WorkspaceID]
		if !seen {
			_, err := h.Store.GetWorkspace(r.Context(), e.WorkspaceID)
			ok = err == nil
			known[e.WorkspaceID] = ok
		}
		if !ok {
			continue // not a workspace of ours: don't let the table fill with junk
		}
		if e.Time.IsZero() {
			e.Time = time.Now().UTC()
		}
		e.Kind, e.Decision = clip(e.Kind, 16), clip(e.Decision, 16)
		e.Host, e.Method, e.Path = clip(e.Host, maxGuardField), clip(e.Method, 16), clip(e.Path, maxGuardField)
		e.Rule, e.Detail = clip(e.Rule, maxGuardField), clip(e.Detail, maxGuardField)
		keep = append(keep, e)
	}
	if err := es.AppendGuardEvents(r.Context(), keep); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// GET /api/workspaces/{id}/activity?limit=100
func (h *Handler) WorkspaceActivity(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || !h.canAccessWorkspace(r, ws) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	es, ok := h.Store.(store.GuardEventStore)
	if !ok {
		h.err(w, http.StatusNotImplemented, "this server's database keeps no network activity")
		return
	}
	limit := 100
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	if limit > maxActivityLimit {
		limit = maxActivityLimit
	}
	evs, err := es.ListGuardEvents(r.Context(), ws.ID, limit)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if evs == nil {
		evs = []models.GuardEvent{}
	}
	h.json(w, http.StatusOK, evs)
}

// GET /api/workspaces/{id}/guard-policy
func (h *Handler) GetGuardPolicy(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || !h.canAccessWorkspace(r, ws) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	pol, guarded := policyOf(ws)
	out := map[string]any{"guarded": guarded, "policy": pol}
	if base, err := h.baseline(r.Context()); err == nil && guarded && !base.IsZero() {
		out["effective"] = base.Apply(pol) // what the gateway enforces after the baseline
	}
	h.json(w, http.StatusOK, out)
}

// PUT /api/workspaces/{id}/guard-policy
// Changes what a guarded workspace may reach; the gateway picks it up within seconds, no
// restart needed. A workspace that was created without the gateway cannot be moved behind
// it afterwards (its network was set up without the proxy), so that is refused.
func (h *Handler) PutGuardPolicy(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || !h.canAccessWorkspace(r, ws) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	if _, guarded := policyOf(ws); !guarded {
		h.err(w, http.StatusConflict, "this workspace was created without the egress gateway; create it with a guard policy instead")
		return
	}
	var pol guard.Policy
	if err := h.decode(r, &pol); err != nil {
		h.err(w, http.StatusBadRequest, "bad request")
		return
	}
	if err := pol.Validate(); err != nil {
		h.err(w, http.StatusBadRequest, err.Error())
		return
	}
	base, err := h.baseline(r.Context())
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := base.Check(pol); err != nil {
		h.err(w, http.StatusForbidden, err.Error())
		return
	}
	setPolicy(ws, pol)
	ws.UpdatedAt = time.Now()
	if err := h.Store.UpdateWorkspace(r.Context(), ws); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, map[string]any{"guarded": true, "policy": pol})
}
