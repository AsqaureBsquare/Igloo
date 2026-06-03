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

// GET /api/workspaces/{id}/forward?token=...&port=22
// WebSocket — raw TCP tunnel to the given port inside the workspace container.
func (h *Handler) Forward(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "id")

	token := r.URL.Query().Get("token")
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

	chanID := uuid.NewString()
	ch, cancel := h.Relay.Subscribe(wsID)
	defer cancel()

	// Ask agent to open TCP connection to localhost:port
	openRaw, _ := json.Marshal(tcpMsg{Type: "tcp_open", ID: chanID, Path: port})
	h.Relay.SendToAgent(wsID, openRaw)

	// Wait for tcp_ready
	ctx, ctxCancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer ctxCancel()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var m tcpMsg
			if json.Unmarshal(msg, &m) == nil && m.Type == "tcp_ready" && m.ID == chanID {
				goto ready
			}
		case <-ctx.Done():
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
