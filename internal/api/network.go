package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// NetConn is a single TCP connection entry from the agent's /proc/net/tcp snapshot.
type NetConn struct {
	LocalAddr  string `json:"local_addr"`
	LocalPort  string `json:"local_port"`
	RemoteAddr string `json:"remote_addr"`
	RemotePort string `json:"remote_port"`
	State      string `json:"state"`
}

// NetSnapshot is the latest network state for one workspace.
type NetSnapshot struct {
	WorkspaceID string    `json:"workspace_id"`
	UpdatedAt   time.Time `json:"updated_at"`
	Connections []NetConn `json:"connections"`
}

// NetMapStore holds the latest network snapshot per workspace, in memory only.
type NetMapStore struct {
	mu   sync.RWMutex
	data map[string]*NetSnapshot
}

func NewNetMapStore() *NetMapStore {
	return &NetMapStore{data: make(map[string]*NetSnapshot)}
}

func (s *NetMapStore) store(workspaceID string, snap *NetSnapshot) {
	s.mu.Lock()
	s.data[workspaceID] = snap
	s.mu.Unlock()
}

func (s *NetMapStore) Get(workspaceID string) *NetSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[workspaceID]
}

func (s *NetMapStore) Delete(workspaceID string) {
	s.mu.Lock()
	delete(s.data, workspaceID)
	s.mu.Unlock()
}

// HandleAgentMessage is registered with the relay; it intercepts net_snapshot
// messages from any agent and stores them without affecting relay forwarding.
func (s *NetMapStore) HandleAgentMessage(workspaceID string, raw []byte) {
	var m struct {
		Type        string    `json:"type"`
		Connections []NetConn `json:"connections"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Type != "net_snapshot" {
		return
	}
	s.store(workspaceID, &NetSnapshot{
		WorkspaceID: workspaceID,
		UpdatedAt:   time.Now(),
		Connections: m.Connections,
	})
}

// GetNetworkMap handles GET /api/workspaces/{id}/network.
func (h *Handler) GetNetworkMap(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	snap := h.NetMap.Get(id)
	if snap == nil {
		h.json(w, http.StatusOK, NetSnapshot{
			WorkspaceID: id,
			UpdatedAt:   time.Now(),
			Connections: []NetConn{},
		})
		return
	}
	h.json(w, http.StatusOK, snap)
}
