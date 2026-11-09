package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/relay"
	"github.com/igloo-dev/igloo/internal/store"
)

// slowStore stalls after each workspace write, widening any gap between "status
// written" and whatever the handler does next.
type slowStore struct {
	store.Store
	delay time.Duration
}

func (s slowStore) UpdateWorkspace(ctx context.Context, w *models.Workspace) error {
	err := s.Store.UpdateWorkspace(ctx, w)
	time.Sleep(s.delay)
	return err
}

func agentRig(t *testing.T, delay time.Duration) (*Handler, *httptest.Server) {
	h := newTestHandler()
	h.Store = slowStore{Store: h.Store, delay: delay}
	h.Relay = relay.New()
	now := time.Now()
	if err := h.Store.CreateWorkspace(context.Background(), &models.Workspace{
		ID: "w1", UserID: "u", Status: models.StatusCreating, CreatedAt: now, UpdatedAt: now, LastActiveAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Get("/ws", h.WebSocket)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return h, srv
}

func dialAgent(t *testing.T, h *Handler, srv *httptest.Server) *websocket.Conn {
	tok, _ := h.Auth.SignConn("w1", "agent", time.Hour)
	c, _, err := websocket.DefaultDialer.Dial("ws"+srv.URL[len("http"):]+"/ws", http.Header{"Authorization": {"Bearer " + tok}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func wsStatus(h *Handler) models.WorkspaceStatus {
	w, err := h.Store.GetWorkspace(context.Background(), "w1")
	if err != nil {
		return ""
	}
	return w.Status
}

// "running" must mean the agent can already be reached, otherwise a client that
// acts on it (open the terminal, start a tunnel) gets "agent not connected".
func TestWorkspaceIsRunningOnlyOnceAgentIsReachable(t *testing.T) {
	h, srv := agentRig(t, 150*time.Millisecond)
	dialAgent(t, h, srv)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if wsStatus(h) == models.StatusRunning {
			if !h.Relay.AgentConnected("w1") {
				t.Fatal("status is running but the relay has no agent yet")
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("workspace never became running")
}

// After a network blip the new agent connection registers before the old one
// closes. Closing the old one must not mark a live workspace asleep.
func TestStaleAgentConnectionClosingDoesNotPutWorkspaceToSleep(t *testing.T) {
	h, srv := agentRig(t, 0)
	old := dialAgent(t, h, srv)
	for i := 0; i < 200 && wsStatus(h) != models.StatusRunning; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	fresh := dialAgent(t, h, srv)
	time.Sleep(150 * time.Millisecond) // the new connection replaces the old in the relay

	old.Close()
	time.Sleep(400 * time.Millisecond)
	if got := wsStatus(h); got != models.StatusRunning {
		t.Fatalf("status = %s after the stale connection closed, want running", got)
	}
	if !h.Relay.AgentConnected("w1") {
		t.Fatal("live agent was dropped")
	}

	fresh.Close() // the last agent leaving does put it to sleep
	for i := 0; i < 200 && wsStatus(h) != models.StatusSleeping; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if got := wsStatus(h); got != models.StatusSleeping {
		t.Errorf("status = %s after the last agent left, want sleeping", got)
	}
}

func TestDeferredJobReturnsToQueueWithoutUsingARetry(t *testing.T) {
	h := newTestHandler()
	ctx := context.Background()
	now := time.Now()
	mk := func(id string, status models.JobStatus, retries int) {
		h.Store.CreateJob(ctx, &models.ProvisionJob{ID: id, WorkspaceID: "w", Action: models.JobStart, Status: status,
			RetryCount: retries, ProvisionerID: "d1", CreatedAt: now, UpdatedAt: now})
	}
	mk("running", models.JobRunning, 1)
	mk("done", models.JobDone, 0)

	h.handleJobDefer(ctx, "running")
	j, _ := h.Store.GetJob(ctx, "running")
	if j.Status != models.JobPending || j.RetryCount != 1 || j.ProvisionerID != "" {
		t.Errorf("deferred job = %s retries=%d owner=%q; want pending, 1 (unchanged), no owner", j.Status, j.RetryCount, j.ProvisionerID)
	}
	// A finished job must not be resurrected by a late message.
	h.handleJobDefer(ctx, "done")
	if d, _ := h.Store.GetJob(ctx, "done"); d.Status != models.JobDone {
		t.Errorf("done job changed to %s", d.Status)
	}
	h.handleJobDefer(ctx, "does-not-exist") // must not panic
}
