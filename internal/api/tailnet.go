package api

import (
	"log"
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
		// The tailnet socket is its own connection and can land on a different replica
		// than the relay's, so it leases its own ownership row.
		h.Relay.ClaimStream("tailnet", wsID)
		h.Coordinator.ServeAgent(wsID, conn)
		// ServeAgent returns when the connection ends. A reconnect lands wherever the
		// Service sends it and claims again; release only while nothing replaced us here.
		if !h.Coordinator.HasAgent(wsID) {
			h.Relay.ReleaseStream("tailnet", wsID)
		}
		return
	}

	// A client on the replica that holds the agent is served locally; otherwise the
	// whole stream is piped to the owner, which treats it as a local client.
	if !h.Coordinator.HasAgent(wsID) {
		if podAddr, self, ok := h.Relay.StreamOwner("tailnet", wsID); ok && !self {
			peer, err := h.Relay.DialPeer(podAddr, "/internal/tailnet/"+wsID)
			if err == nil {
				pipeWS(conn, peer)
				return
			}
			log.Printf("[tailnet] dial owner for workspace=%s: %v", wsID, err)
		}
	}
	h.Coordinator.ServeClient(wsID, conn)
}

// pipeWS joins two WebSockets, preserving frame types, until either side closes.
// Each connection is read by one goroutine and written by the other, so the
// one-concurrent-reader / one-concurrent-writer rule holds.
func pipeWS(a, b *websocket.Conn) {
	done := make(chan struct{}, 2)
	cp := func(src, dst *websocket.Conn) {
		for {
			mt, data, err := src.ReadMessage()
			if err != nil {
				break
			}
			if dst.WriteMessage(mt, data) != nil {
				break
			}
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done // one direction ended; closing both unblocks the other
	a.Close()
	b.Close()
	<-done
}
