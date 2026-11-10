package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

var tnUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// GET /api/workspaces/{id}/tailnet?role=agent|client&token=<jwt>
//
// Agents use their long-lived agent JWT (role="agent").
// CLI clients use a short-lived forward JWT (role="forward") obtained from
// GET /api/workspaces/{id}/forward-token — the same token used by igloo proxy.
func (h *Handler) TailnetWS(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "id")
	role := r.URL.Query().Get("role")
	token := connToken(r)

	claims, err := h.Auth.ParseConn(token)
	if err != nil || claims.WorkspaceID != wsID {
		h.err(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	switch role {
	case "agent":
		if claims.Role != "agent" {
			h.err(w, http.StatusForbidden, "agent token required")
			return
		}
	case "client":
		if claims.Role != "forward" {
			h.err(w, http.StatusForbidden, "forward token required")
			return
		}
	default:
		h.err(w, http.StatusBadRequest, "role must be agent or client")
		return
	}

	conn, err := tnUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxClientFrame)

	if role == "agent" {
		h.Coordinator.ServeAgent(wsID, conn)
	} else {
		h.Coordinator.ServeClient(wsID, conn)
	}
}
