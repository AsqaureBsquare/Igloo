package api

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func dialRole(t *testing.T, h *Handler, srvURL, role string) *websocket.Conn {
	tok, _ := h.Auth.SignConn("w1", role, time.Hour)
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srvURL, "http")+"/ws", http.Header{"Authorization": {"Bearer " + tok}})
	if err != nil {
		t.Fatalf("dial %s: %v", role, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// expectClosed1009 reads until the server closes the connection and checks it was
// for message size (status 1009), not some other failure.
func expectClosed1009(t *testing.T, c *websocket.Conn, what string) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			ce, ok := err.(*websocket.CloseError)
			if !ok || ce.Code != websocket.CloseMessageTooBig {
				t.Fatalf("%s: connection ended with %v, want a 1009 close", what, err)
			}
			return
		}
	}
}

func TestBrowserFramesAboveTheLimitCloseTheConnection(t *testing.T) {
	h, srv := agentRig(t, 0)
	c := dialRole(t, h, srv.URL, "browser")

	// Within the limit: accepted and answered ("agent not connected" comes back because
	// no agent is attached in this test).
	if err := c.WriteMessage(websocket.TextMessage, bytes.Repeat([]byte("a"), 3<<20)); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, msg, err := c.ReadMessage(); err != nil || !strings.Contains(string(msg), "agent not connected") {
		t.Fatalf("a 3 MiB browser message should be accepted and answered; got %q, %v", msg, err)
	}

	// Over it: cut off.
	c.WriteMessage(websocket.TextMessage, bytes.Repeat([]byte("a"), maxBrowserFrame+1))
	expectClosed1009(t, c, "oversized browser frame")
}

// An agent legitimately sends big messages (a proxied HTTP response), so its limit is
// far above a browser's, but still finite.
func TestAgentFramesHaveAHigherButFiniteLimit(t *testing.T) {
	h, srv := agentRig(t, 0)
	ch, cancel := h.Relay.Subscribe("w1")
	defer cancel()
	agent := dialRole(t, h, srv.URL, "agent")
	for i := 0; i < 200 && !h.Relay.AgentConnected("w1"); i++ {
		time.Sleep(10 * time.Millisecond)
	}

	// 5 MiB: over a browser's limit, fine for an agent.
	big := bytes.Repeat([]byte("b"), 5<<20)
	if err := agent.WriteMessage(websocket.TextMessage, big); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ch:
		if len(got) != len(big) {
			t.Errorf("relayed %d bytes, want %d", len(got), len(big))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a 5 MiB agent message was not relayed")
	}

	agent.WriteMessage(websocket.TextMessage, bytes.Repeat([]byte("c"), maxAgentFrame+1))
	expectClosed1009(t, agent, "oversized agent frame")
}

func TestPortForwardClientFramesAboveTheLimitCloseTheTunnel(t *testing.T) {
	f := newForwardRig(t)
	f.startAgent(t, "w1", false)
	c := f.dial(t, "w1", "13131")

	c.WriteMessage(websocket.BinaryMessage, bytes.Repeat([]byte("x"), maxClientFrame+1))
	expectClosed1009(t, c, "oversized port-forward frame")
}
