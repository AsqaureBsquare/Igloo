package api

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/relay"
)

// forwardRig serves the real Forward handler plus an endpoint that registers a
// fake workspace agent with the relay, so the whole tunnel path is exercised.
type forwardRig struct {
	h   *Handler
	srv *httptest.Server
}

func newForwardRig(t *testing.T) *forwardRig {
	h := newTestHandler()
	h.Relay = relay.New()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	r := chi.NewRouter()
	r.Get("/agent/{id}", func(w http.ResponseWriter, req *http.Request) {
		c, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		h.Relay.RegisterAgent(chi.URLParam(req, "id"), "agent", c)
	})
	r.Get("/api/workspaces/{id}/forward", h.Forward)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &forwardRig{h: h, srv: srv}
}

func wsBase(srv *httptest.Server) string { return "ws" + strings.TrimPrefix(srv.URL, "http") }

// startAgent connects a fake agent that answers tcp_open per refuse, and echoes
// tcp_data upper-cased, like a service listening on the port would.
func (f *forwardRig) startAgent(t *testing.T, wsID string, refuse bool) {
	c, _, err := websocket.DefaultDialer.Dial(wsBase(f.srv)+"/agent/"+wsID, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		for {
			var m tcpMsg
			if err := c.ReadJSON(&m); err != nil {
				return
			}
			switch m.Type {
			case "tcp_open":
				if refuse {
					c.WriteJSON(tcpMsg{Type: "tcp_close", ID: m.ID})
				} else {
					c.WriteJSON(tcpMsg{Type: "tcp_ready", ID: m.ID})
				}
			case "tcp_data":
				b, _ := base64.StdEncoding.DecodeString(m.Data)
				c.WriteJSON(tcpMsg{Type: "tcp_data", ID: m.ID, Data: base64.StdEncoding.EncodeToString([]byte(strings.ToUpper(string(b))))})
			}
		}
	}()
	for i := 0; i < 200 && !f.h.Relay.AgentConnected(wsID); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !f.h.Relay.AgentConnected(wsID) {
		t.Fatal("fake agent never registered")
	}
}

func (f *forwardRig) dial(t *testing.T, wsID, port string) *websocket.Conn {
	tok, _ := f.h.Auth.SignConn(wsID, "forward", time.Minute)
	c, _, err := websocket.DefaultDialer.Dial(wsBase(f.srv)+"/api/workspaces/"+wsID+"/forward?port="+port,
		http.Header{"Authorization": {"Bearer " + tok}})
	if err != nil {
		t.Fatalf("dial forward: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// When the agent can't reach the port the client must learn why straight away,
// not after the 10 s handshake timeout and with no explanation.
func TestForwardReportsClosedPortImmediately(t *testing.T) {
	f := newForwardRig(t)
	f.startAgent(t, "w1", true)
	c := f.dial(t, "w1", "13131")

	start := time.Now()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := c.ReadMessage()
	ce, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("got %v, want a WebSocket close frame", err)
	}
	if ce.Code != websocket.CloseTryAgainLater || !strings.Contains(ce.Text, "port 13131") {
		t.Errorf("close = %d %q, want 1013 mentioning the port", ce.Code, ce.Text)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v to report a refused port", time.Since(start))
	}
}

func TestForwardCarriesDataBothWays(t *testing.T) {
	f := newForwardRig(t)
	f.startAgent(t, "w1", false)
	c := f.dial(t, "w1", "13131")

	if err := c.WriteMessage(websocket.BinaryMessage, []byte("hello igloo")); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	kind, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.BinaryMessage || string(msg) != "HELLO IGLOO" {
		t.Errorf("got %v %q, want binary \"HELLO IGLOO\"", kind, msg)
	}
}

func TestForwardRejectsMissingAgentAndBadToken(t *testing.T) {
	f := newForwardRig(t)
	tok, _ := f.h.Auth.SignConn("w1", "forward", time.Minute)
	get := func(path, bearer string) int {
		req, _ := http.NewRequest("GET", f.srv.URL+path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := get("/api/workspaces/w1/forward", tok); got != http.StatusServiceUnavailable {
		t.Errorf("no agent connected: %d, want 503", got)
	}
	if got := get("/api/workspaces/w1/forward", "bogus"); got != http.StatusUnauthorized {
		t.Errorf("bad token: %d, want 401", got)
	}
}
