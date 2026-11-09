package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// harness serves /agent and /browser WebSocket endpoints backed by one Relay.
type harness struct {
	r   *Relay
	srv *httptest.Server
}

func newHarness(t *testing.T) *harness {
	h := &harness{r: New()}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		ws_id, conn := req.URL.Query().Get("ws"), req.URL.Query().Get("conn")
		if strings.HasPrefix(req.URL.Path, "/agent") {
			h.r.RegisterAgent(ws_id, conn, ws)
		} else {
			h.r.RegisterBrowser(ws_id, conn, ws)
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) dial(t *testing.T, kind, ws, conn string) *websocket.Conn {
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/" + kind + "?ws=" + ws + "&conn=" + conn
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", kind, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func read(t *testing.T, c *websocket.Conn) string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(msg)
}

func TestRoutesBetweenBrowserAndAgent(t *testing.T) {
	h := newHarness(t)
	agent := h.dial(t, "agent", "w1", "a")
	waitFor(t, "agent registered", func() bool { return h.r.AgentConnected("w1") })
	browser := h.dial(t, "browser", "w1", "b")
	waitFor(t, "browser registered", func() bool {
		h.r.mu.RLock()
		defer h.r.mu.RUnlock()
		return len(h.r.browsers["w1"]) == 1
	})

	browser.WriteMessage(websocket.TextMessage, []byte("ls\n"))
	if got := read(t, agent); got != "ls\n" {
		t.Errorf("agent got %q", got)
	}
	out := `{"type":"pty_output","data":"b3V0cHV0"}`
	agent.WriteMessage(websocket.TextMessage, []byte(out))
	if got := read(t, browser); got != out {
		t.Errorf("browser got %q", got)
	}
}

// What an agent sends in answer to the server (command results, file contents, forwarded web
// responses, tunnel data) must never reach a browser that happens to have a terminal open.
func TestOnlyTerminalMessagesReachBrowsers(t *testing.T) {
	h := newHarness(t)
	agent := h.dial(t, "agent", "w1", "a")
	waitFor(t, "agent registered", func() bool { return h.r.AgentConnected("w1") })
	browser := h.dial(t, "browser", "w1", "b")
	waitFor(t, "browser registered", func() bool {
		h.r.mu.RLock()
		defer h.r.mu.RUnlock()
		return len(h.r.browsers["w1"]) == 1
	})
	sub, cancel := h.r.Subscribe("w1")
	defer cancel()
	var handled []string
	var hmu sync.Mutex
	h.r.OnAgentMessage(func(_ string, msg []byte) { hmu.Lock(); handled = append(handled, string(msg)); hmu.Unlock() })

	private := []string{
		`{"type":"exec_res","id":"1","stdout":"c2VjcmV0"}`,
		`{"type":"fs_res","id":"2","data":"a2V5"}`,
		`{"type":"http_res","id":"3","status":200}`,
		`{"type":"tcp_data","id":"4","data":"eA=="}`,
		`{"type":"ws_data","id":"5"}`,
		`{"type":"net_snapshot"}`,
		`not json at all`,
		`{"type":"pty_outputX"}`,
		`{}`,
	}
	for _, m := range private {
		agent.WriteMessage(websocket.TextMessage, []byte(m))
	}
	terminal := []string{`{"type":"pty_ready"}`, `{"type":"pty_output","data":"aGk="}`, `{"type":"pty_exit"}`, `{"type":"pty_error","data":"x"}`}
	for _, m := range terminal {
		agent.WriteMessage(websocket.TextMessage, []byte(m))
	}

	for _, want := range terminal { // the browser sees the terminal messages, in order, and nothing before them
		if got := read(t, browser); got != want {
			t.Fatalf("browser got %q, want %q (a private message leaked, or order changed)", got, want)
		}
	}
	for _, want := range private { // the server side still gets every one of them
		select {
		case got := <-sub:
			if string(got) != want {
				t.Fatalf("subscriber got %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber never got %q", want)
		}
	}
	waitFor(t, "handlers", func() bool { hmu.Lock(); defer hmu.Unlock(); return len(handled) == len(private)+len(terminal) })
}

func TestWorkspacesAreIsolated(t *testing.T) {
	h := newHarness(t)
	h.dial(t, "agent", "w1", "a1")
	agent2 := h.dial(t, "agent", "w2", "a2")
	waitFor(t, "agents", func() bool { return h.r.AgentConnected("w1") && h.r.AgentConnected("w2") })
	browser1 := h.dial(t, "browser", "w1", "b1")
	waitFor(t, "browser", func() bool {
		h.r.mu.RLock()
		defer h.r.mu.RUnlock()
		return len(h.r.browsers["w1"]) == 1
	})

	browser1.WriteMessage(websocket.TextMessage, []byte("secret"))
	agent2.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, msg, err := agent2.ReadMessage(); err == nil {
		t.Errorf("workspace 2's agent received workspace 1's traffic: %q", msg)
	}
}

func TestBrowserWithoutAgentGetsError(t *testing.T) {
	h := newHarness(t)
	browser := h.dial(t, "browser", "w1", "b")
	waitFor(t, "browser", func() bool {
		h.r.mu.RLock()
		defer h.r.mu.RUnlock()
		return len(h.r.browsers["w1"]) == 1
	})
	browser.WriteMessage(websocket.TextMessage, []byte("hi"))
	if got := read(t, browser); !strings.Contains(got, "agent not connected") {
		t.Errorf("got %q, want a pty_error", got)
	}
}

func TestAgentReconnectReplacesOldWithoutDroppingNew(t *testing.T) {
	h := newHarness(t)
	old := h.dial(t, "agent", "w1", "a-old")
	waitFor(t, "first agent", func() bool { return h.r.AgentConnected("w1") })
	h.dial(t, "agent", "w1", "a-new")
	time.Sleep(100 * time.Millisecond) // new registration replaces the map entry
	old.Close()                        // the old connection's teardown must not evict the new agent
	time.Sleep(200 * time.Millisecond)
	if !h.r.AgentConnected("w1") {
		t.Error("closing the stale agent connection removed the live one")
	}
}

func TestDisconnectCleansUp(t *testing.T) {
	h := newHarness(t)
	agent := h.dial(t, "agent", "w1", "a")
	waitFor(t, "agent", func() bool { return h.r.AgentConnected("w1") })
	agent.Close()
	waitFor(t, "agent removal", func() bool { return !h.r.AgentConnected("w1") })
}

func TestSubscribeReceivesAgentMessagesUntilCancelled(t *testing.T) {
	h := newHarness(t)
	agent := h.dial(t, "agent", "w1", "a")
	waitFor(t, "agent", func() bool { return h.r.AgentConnected("w1") })
	ch, cancel := h.r.Subscribe("w1")
	agent.WriteMessage(websocket.TextMessage, []byte("one"))
	select {
	case m := <-ch:
		if string(m) != "one" {
			t.Errorf("got %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber got nothing")
	}
	cancel()
	h.r.mu.RLock()
	n := len(h.r.subs["w1"])
	h.r.mu.RUnlock()
	if n != 0 {
		t.Errorf("%d subscribers left after cancel", n)
	}
}

// SendToAgent races with the agent disconnecting. It must never panic.
func TestSendToAgentWhileAgentDisconnectsDoesNotPanic(t *testing.T) {
	h := newHarness(t)
	for round := 0; round < 60; round++ {
		agent := h.dial(t, "agent", "w1", "a")
		waitFor(t, "agent", func() bool { return h.r.AgentConnected("w1") })

		var wg sync.WaitGroup
		stop := make(chan struct{})
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						h.r.SendToAgent("w1", []byte("x"))
					}
				}
			}()
		}
		time.Sleep(time.Millisecond)
		agent.Close()
		waitFor(t, "agent removal", func() bool { return !h.r.AgentConnected("w1") })
		time.Sleep(2 * time.Millisecond) // keep senders running across the close(send) window
		close(stop)
		wg.Wait()
	}
}

func TestEmptyPerWorkspaceMapsAreReleased(t *testing.T) {
	h := newHarness(t)
	browser := h.dial(t, "browser", "w1", "b")
	waitFor(t, "browser", func() bool {
		h.r.mu.RLock()
		defer h.r.mu.RUnlock()
		return len(h.r.browsers["w1"]) == 1
	})
	_, cancel := h.r.Subscribe("w1")
	cancel()
	browser.Close()
	waitFor(t, "cleanup", func() bool {
		h.r.mu.RLock()
		defer h.r.mu.RUnlock()
		return len(h.r.browsers) == 0 && len(h.r.subs) == 0
	})
}

func TestRegisterAgentNotifyRunsOnlyOnceAgentIsReachable(t *testing.T) {
	r := New()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	reachable := make(chan bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		r.RegisterAgentNotify("w1", "a", ws, func() { reachable <- r.AgentConnected("w1") })
	}))
	defer srv.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case ok := <-reachable:
		if !ok {
			t.Error("callback ran before the agent was registered")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callback never ran")
	}
}
