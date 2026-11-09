package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/models"
)

const (
	logsTicketRole = "logs"
	logsTicketTTL  = 30 * time.Second
)

type tcpMsg struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Path string `json:"path,omitempty"` // port number as string for tcp_open
	Data string `json:"data,omitempty"` // base64
}

// GET /api/workspaces/{id}/forward-token
func (h *Handler) ForwardToken(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || ws.UserID != ctxUserID(r) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	if ws.Status != models.StatusRunning {
		h.err(w, http.StatusConflict, "workspace is not running")
		return
	}
	token, err := h.Auth.SignConn(ws.ID, "forward", 24*time.Hour)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, map[string]string{"token": token})
}

// GET /api/workspaces/{id}/logs-ticket
// Issues a 30-second token that can only open this workspace's provision-log
// stream. The browser's EventSource can't set headers, so the ticket goes in the
// URL instead of the long-lived session token.
func (h *Handler) LogsTicket(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || ws.UserID != ctxUserID(r) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	ticket, err := h.Auth.SignConn(ws.ID, logsTicketRole, logsTicketTTL)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, map[string]string{"ticket": ticket})
}

// GET /api/workspaces/{id}/forward?token=...&port=22
// WebSocket — raw TCP tunnel to the given port inside the workspace container.
func (h *Handler) Forward(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "id")

	token := connToken(r)
	claims, err := h.Auth.ParseConn(token)
	if err != nil || claims.WorkspaceID != wsID || claims.Role != "forward" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	port := r.URL.Query().Get("port")
	if port == "" {
		port = "22"
	}

	if !h.Relay.AgentConnected(wsID) {
		http.Error(w, "workspace agent not connected", http.StatusServiceUnavailable)
		return
	}

	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[forward] ws upgrade: %v", err)
		return
	}
	defer clientConn.Close()
	clientConn.SetReadLimit(maxClientFrame)

	chanID := uuid.NewString()
	ch, cancel := h.Relay.Subscribe(wsID)
	defer cancel()

	// Ask agent to open TCP connection to localhost:port
	openRaw, _ := json.Marshal(tcpMsg{Type: "tcp_open", ID: chanID, Path: port})
	h.Relay.SendToAgent(wsID, openRaw)

	// Wait for tcp_ready. The agent answers tcp_close instead when it can't reach the
	// port (for example code-server is still starting): say so at once, instead of
	// leaving the client waiting out the timeout with no explanation.
	ctx, ctxCancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer ctxCancel()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var m tcpMsg
			if json.Unmarshal(msg, &m) != nil || m.ID != chanID {
				continue
			}
			if m.Type == "tcp_ready" {
				goto ready
			}
			if m.Type == "tcp_close" {
				closeForward(clientConn, "workspace could not open port "+port+" (is the service running yet?)")
				return
			}
		case <-ctx.Done():
			closeForward(clientConn, "workspace agent did not answer within 10s")
			return
		}
	}
ready:
	ctxCancel()

	runCtx, runCancel := context.WithCancel(r.Context())
	defer runCancel()

	// CLI → agent (TCP)
	go func() {
		defer runCancel()
		for {
			_, data, err := clientConn.ReadMessage()
			if err != nil {
				break
			}
			m := tcpMsg{Type: "tcp_data", ID: chanID, Data: base64.StdEncoding.EncodeToString(data)}
			raw, _ := json.Marshal(m)
			h.Relay.SendToAgent(wsID, raw)
		}
		closeRaw, _ := json.Marshal(tcpMsg{Type: "tcp_close", ID: chanID})
		h.Relay.SendToAgent(wsID, closeRaw)
	}()

	// Agent (TCP) → CLI
	for {
		select {
		case agentMsg, ok := <-ch:
			if !ok {
				return
			}
			var m tcpMsg
			if json.Unmarshal(agentMsg, &m) != nil || m.ID != chanID {
				continue
			}
			switch m.Type {
			case "tcp_data":
				data, _ := base64.StdEncoding.DecodeString(m.Data)
				clientConn.WriteMessage(websocket.BinaryMessage, data)
			case "tcp_close":
				return
			}
		case <-runCtx.Done():
			return
		}
	}
}

// closeForward ends a tunnel with a WebSocket close frame that carries the reason
// (code 1013, "try again later"), so clients can show it.
func closeForward(c *websocket.Conn, reason string) {
	c.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseTryAgainLater, reason),
		time.Now().Add(2*time.Second))
}
