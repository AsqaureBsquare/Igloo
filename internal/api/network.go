package api

import (
	"context"
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

// SnapshotStore is the slice of the store the netmap needs; store.NetSnapshotStore
// satisfies it.
type SnapshotStore interface {
	SaveNetSnapshot(ctx context.Context, workspaceID string, connections []byte, updatedAt time.Time) error
	GetNetSnapshot(ctx context.Context, workspaceID string) ([]byte, time.Time, error)
	DeleteNetSnapshot(ctx context.Context, workspaceID string) error
}

// NetMapStore holds the latest network snapshot per workspace. The agent reports to one
// replica, which keeps the snapshot in memory and persists it; a replica that has not
// heard from the agent answers from the store. Persisting is coalesced in a background
// flusher so the agent's read loop never waits on the database.
type NetMapStore struct {
	mu      sync.RWMutex
	data    map[string]*NetSnapshot
	store   SnapshotStore
	pending map[string]*NetSnapshot // latest snapshot not yet persisted
}

func NewNetMapStore() *NetMapStore {
	return &NetMapStore{data: make(map[string]*NetSnapshot), pending: make(map[string]*NetSnapshot)}
}

// Back makes snapshots outlive this replica. Call once, before the store serves.
func (s *NetMapStore) Back(st SnapshotStore) {
	s.store = st
	go s.flushLoop()
}

func (s *NetMapStore) flushLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		batch := s.pending
		s.pending = make(map[string]*NetSnapshot)
		s.mu.Unlock()
		for id, snap := range batch {
			raw, err := json.Marshal(snap.Connections)
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = s.store.SaveNetSnapshot(ctx, id, raw, snap.UpdatedAt)
			cancel()
		}
	}
}

func (s *NetMapStore) storeSnap(workspaceID string, snap *NetSnapshot) {
	s.mu.Lock()
	s.data[workspaceID] = snap
	if s.store != nil {
		s.pending[workspaceID] = snap
	}
	s.mu.Unlock()
}

func (s *NetMapStore) Get(workspaceID string) *NetSnapshot {
	s.mu.RLock()
	snap := s.data[workspaceID]
	st := s.store
	s.mu.RUnlock()
	if snap != nil || st == nil {
		return snap
	}
	// This replica never heard from the agent — another one did.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, updatedAt, err := st.GetNetSnapshot(ctx, workspaceID)
	if err != nil {
		return nil
	}
	var conns []NetConn
	if json.Unmarshal(raw, &conns) != nil {
		return nil
	}
	return &NetSnapshot{WorkspaceID: workspaceID, UpdatedAt: updatedAt, Connections: conns}
}

func (s *NetMapStore) Delete(workspaceID string) {
	s.mu.Lock()
	delete(s.data, workspaceID)
	delete(s.pending, workspaceID)
	st := s.store
	s.mu.Unlock()
	if st != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = st.DeleteNetSnapshot(ctx, workspaceID)
	}
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
	s.storeSnap(workspaceID, &NetSnapshot{
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
