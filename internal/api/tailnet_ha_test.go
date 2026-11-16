package api

// The WireGuard path across replicas: the agent's tailnet socket lands on one replica,
// the CLI client's on another, and the client must still learn the agent's key and
// exchange packets with it.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/auth"
	"github.com/igloo-dev/igloo/internal/relay"
	"github.com/igloo-dev/igloo/internal/store"
	"github.com/igloo-dev/igloo/internal/tailnet"
)

type tailnetReplica struct {
	h   *Handler
	srv *httptest.Server
}

func newTailnetReplica(t *testing.T, st store.Store, au *auth.Auth, id string) *tailnetReplica {
	t.Helper()
	h := &Handler{Store: st, Auth: au, Relay: relay.New(), Coordinator: tailnet.NewCoordinator()}

	imux := http.NewServeMux()
	imux.Handle("/internal/relay/", h.Relay.InternalHandler())
	imux.Handle("/internal/tailnet/", h.Relay.InternalStreamHandler(func(wsID string, ws *websocket.Conn) {
		h.Coordinator.ServeClient(wsID, ws)
	}))
	internal := httptest.NewServer(imux)
	t.Cleanup(internal.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.Relay.JoinCluster(ctx, relay.Cluster{
		ReplicaID: id,
		PodAddr:   strings.TrimPrefix(internal.URL, "http://"),
		Key:       "test-cluster-key",
		Owners:    st.(relay.OwnerStore),
	})

	r := chi.NewRouter()
	r.Get("/api/workspaces/{id}/tailnet", h.TailnetWS)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &tailnetReplica{h: h, srv: srv}
}

func (r *tailnetReplica) dialTailnet(t *testing.T, wsID, role, token string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(r.srv.URL, "http") +
		"/api/workspaces/" + wsID + "/tailnet?role=" + role + "&token=" + token
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial tailnet %s: %v", role, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func sendJSON(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, c *websocket.Conn, what string) map[string]any {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("reading %s: %v", what, err)
		}
		if mt != websocket.TextMessage {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("%s is not JSON: %s", what, data)
		}
		return m
	}
}

func readBinary(t *testing.T, c *websocket.Conn, what string) []byte {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("reading %s: %v", what, err)
		}
		if mt == websocket.BinaryMessage {
			return data
		}
	}
}

func TestTailnetClientReachesAnAgentOnAnotherReplica(t *testing.T) {
	st := store.NewMemory()
	au := auth.New("test-secret")
	a := newTailnetReplica(t, st, au, "replica-a")
	b := newTailnetReplica(t, st, au, "replica-b")

	agentTok, _ := au.SignConn("w1", "agent", time.Minute)
	fwdTok, _ := au.SignConn("w1", "forward", time.Minute)

	agent := a.dialTailnet(t, "w1", "agent", agentTok)
	sendJSON(t, agent, map[string]string{"type": "hello", "pubkey": "AGENT-KEY"})
	if cfg := readJSON(t, agent, "agent config"); cfg["type"] != "config" || cfg["ip"] == "" {
		t.Fatalf("agent config = %v", cfg)
	}
	waitFor2(t, "the tailnet ownership row", func() bool {
		_, self, ok := b.h.Relay.StreamOwner("tailnet", "w1")
		return ok && !self
	})

	client := b.dialTailnet(t, "w1", "client", fwdTok)
	sendJSON(t, client, map[string]string{"type": "hello", "pubkey": "CLIENT-KEY"})

	// Control plane crossed: the client's config names the agent on the other replica.
	cfg := readJSON(t, client, "client config")
	if cfg["peer_key"] != "AGENT-KEY" {
		t.Fatalf("client config lacks the agent's key: %v", cfg)
	}
	// ...and the agent learns the client's key.
	if m := readJSON(t, agent, "peer notice"); m["peer_key"] != "CLIENT-KEY" {
		t.Fatalf("agent's peer notice = %v", m)
	}

	// Data plane: encrypted packets cross both ways.
	client.WriteMessage(websocket.BinaryMessage, []byte{1, 2, 3})
	if got := readBinary(t, agent, "packet at agent"); string(got) != string([]byte{1, 2, 3}) {
		t.Fatalf("agent got % x", got)
	}
	agent.WriteMessage(websocket.BinaryMessage, []byte{9, 9})
	if got := readBinary(t, client, "packet at client"); string(got) != string([]byte{9, 9}) {
		t.Fatalf("client got % x", got)
	}
}

// The ownership row must go when the agent's tailnet socket closes, so clients stop
// being piped to a replica that no longer holds anything.
func TestTailnetOwnershipIsReleasedOnDisconnect(t *testing.T) {
	st := store.NewMemory()
	au := auth.New("test-secret")
	a := newTailnetReplica(t, st, au, "replica-a")
	b := newTailnetReplica(t, st, au, "replica-b")

	agentTok, _ := au.SignConn("w1", "agent", time.Minute)
	agent := a.dialTailnet(t, "w1", "agent", agentTok)
	sendJSON(t, agent, map[string]string{"type": "hello", "pubkey": "AGENT-KEY"})
	readJSON(t, agent, "agent config")
	waitFor2(t, "ownership", func() bool { _, _, ok := b.h.Relay.StreamOwner("tailnet", "w1"); return ok })

	agent.Close()
	waitFor2(t, "release", func() bool { _, _, ok := b.h.Relay.StreamOwner("tailnet", "w1"); return !ok })
}

func waitFor2(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
