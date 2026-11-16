package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/store"
)

// replica is one in-process server replica: a relay joined to the cluster, an internal
// listener peers dial, and an endpoint agents connect to.
type replica struct {
	r        *Relay
	agentSrv *httptest.Server
}

func newReplica(t *testing.T, owners OwnerStore, id, key string, lease, renew time.Duration) *replica {
	t.Helper()
	rep := &replica{r: New()}

	internal := httptest.NewServer(rep.r.InternalHandler())
	t.Cleanup(internal.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rep.r.JoinCluster(ctx, Cluster{
		ReplicaID: id,
		PodAddr:   strings.TrimPrefix(internal.URL, "http://"),
		Key:       key,
		Owners:    owners,
		Lease:     lease,
		Renew:     renew,
	})

	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	rep.agentSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		if req.URL.Query().Get("role") == "browser" {
			rep.r.RegisterBrowser(req.URL.Query().Get("ws"), randomID(), ws)
			return
		}
		rep.r.RegisterAgent(req.URL.Query().Get("ws"), randomID(), ws)
	}))
	t.Cleanup(rep.agentSrv.Close)
	return rep
}

func (rep *replica) dial(t *testing.T, role, wsID string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(rep.agentSrv.URL, "http") + "/?role=" + role + "&ws=" + wsID
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", role, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func twoReplicas(t *testing.T, lease, renew time.Duration) (*replica, *replica, OwnerStore) {
	t.Helper()
	owners := store.NewMemory()
	key := "test-cluster-key"
	return newReplica(t, owners, "replica-a", key, lease, renew),
		newReplica(t, owners, "replica-b", key, lease, renew),
		owners
}

// The exec/files/HTTP shape: a request lands on the replica that does not hold the
// agent, and both the request and the agent's answer cross the replica boundary.
func TestRequestsReachAnAgentHeldByAnotherReplica(t *testing.T) {
	a, b, _ := twoReplicas(t, 0, 0)
	agent := a.dial(t, "agent", "w1")
	waitFor(t, "agent on A", func() bool { return a.r.AgentConnected("w1") })

	if !b.r.AgentConnected("w1") {
		t.Fatal("replica B does not see the agent replica A holds")
	}

	sub, cancel := b.r.Subscribe("w1")
	defer cancel()
	req := `{"type":"exec_req","id":"1","cmd":"ls"}`
	b.r.SendToAgent("w1", []byte(req))
	if got := read(t, agent); got != req {
		t.Fatalf("agent got %q, want %q", got, req)
	}

	res := `{"type":"exec_res","id":"1","stdout":"aGk="}`
	agent.WriteMessage(websocket.TextMessage, []byte(res))
	select {
	case got := <-sub:
		if string(got) != res {
			t.Fatalf("subscriber on B got %q, want %q", got, res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the agent's answer never reached replica B")
	}
}

// Forwarding must not become a way around the browser output rules: a browser connected
// to the non-owning replica still sees terminal messages only.
func TestForwardedStreamsKeepTheBrowserFilter(t *testing.T) {
	a, b, _ := twoReplicas(t, 0, 0)
	agent := a.dial(t, "agent", "w1")
	waitFor(t, "agent on A", func() bool { return a.r.AgentConnected("w1") })
	browser := b.dial(t, "browser", "w1")
	waitFor(t, "browser on B", func() bool {
		b.r.mu.RLock()
		defer b.r.mu.RUnlock()
		return len(b.r.browsers["w1"]) == 1
	})
	// Typing in the browser crosses to the agent and opens the forwarding link.
	browser.WriteMessage(websocket.TextMessage, []byte("ls\n"))
	if got := read(t, agent); got != "ls\n" {
		t.Fatalf("agent got %q", got)
	}

	agent.WriteMessage(websocket.TextMessage, []byte(`{"type":"exec_res","id":"9","stdout":"c2VjcmV0"}`))
	out := `{"type":"pty_output","data":"aGk="}`
	agent.WriteMessage(websocket.TextMessage, []byte(out))
	if got := read(t, browser); got != out {
		t.Fatalf("browser got %q, want %q — a private message crossed the replica boundary to a browser", got, out)
	}
}

func TestOwnershipIsReleasedWhenTheAgentDisconnects(t *testing.T) {
	a, b, _ := twoReplicas(t, 0, 0)
	agent := a.dial(t, "agent", "w1")
	waitFor(t, "agent on A", func() bool { return a.r.AgentConnected("w1") })
	waitFor(t, "B sees it", func() bool { return b.r.AgentConnected("w1") })

	agent.Close()
	waitFor(t, "B sees the agent go", func() bool { return !b.r.AgentConnected("w1") })

	sub, cancel := b.r.Subscribe("w1")
	defer cancel()
	b.r.SendToAgent("w1", []byte(`{"type":"exec_req","id":"2"}`))
	select {
	case got := <-sub:
		if !strings.Contains(string(got), "agent not connected") {
			t.Fatalf("got %q, want a clean pty_error", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no error came back for a command to a gone agent")
	}
}

func TestPeerStreamNeedsTheSharedKey(t *testing.T) {
	a, _, _ := twoReplicas(t, 0, 0)
	internal := httptest.NewServer(a.r.InternalHandler())
	defer internal.Close()

	url := "ws" + strings.TrimPrefix(internal.URL, "http") + "/internal/relay/w1"
	hdr := http.Header{"Authorization": []string{"Bearer wrong-key"}}
	if _, resp, err := websocket.DefaultDialer.Dial(url, hdr); err == nil {
		t.Fatal("a peer with the wrong key was let in")
	} else if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key: err=%v resp=%v, want 401", err, resp)
	}
	if _, resp, err := websocket.DefaultDialer.Dial(url, nil); err == nil {
		t.Fatal("a peer with no key was let in")
	} else if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no key: err=%v resp=%v, want 401", err, resp)
	}
}

// A crashed replica cannot delete its rows; the lease must expire on its own, and
// renewal must keep a live replica's rows alive well past one lease.
func TestLeasesExpireUnrenewedAndSurviveRenewed(t *testing.T) {
	t.Run("a dead replica's lease lapses", func(t *testing.T) {
		// Renewal far beyond the test: registering writes the only lease this test sees.
		a, b, _ := twoReplicas(t, 200*time.Millisecond, time.Hour)
		a.dial(t, "agent", "w1")
		waitFor(t, "B sees the agent", func() bool { return b.r.AgentConnected("w1") })
		waitFor(t, "the lease to lapse", func() bool { return !b.r.AgentConnected("w1") })
	})
	t.Run("a live replica's lease is renewed", func(t *testing.T) {
		a, b, _ := twoReplicas(t, 200*time.Millisecond, 50*time.Millisecond)
		a.dial(t, "agent", "w1")
		waitFor(t, "B sees the agent", func() bool { return b.r.AgentConnected("w1") })
		time.Sleep(600 * time.Millisecond) // three lease lifetimes
		if !b.r.AgentConnected("w1") {
			t.Fatal("the lease lapsed although the owner was renewing it")
		}
	})
}
