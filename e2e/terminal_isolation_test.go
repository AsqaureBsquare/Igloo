//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A command run through the sandbox API returns its output to the caller only. Someone with a
// terminal open on the same workspace must not see it (it used to be copied to every browser).
func TestExecOutputDoesNotReachAnOpenTerminal(t *testing.T) {
	c := guardedLogin(t)
	var sb struct{ ID string }
	c.json("POST", "/api/sandboxes", map[string]any{"name": "leak-check", "ttl_seconds": 300, "image": image}, 201, &sb)
	t.Cleanup(func() { c.do("DELETE", "/api/sandboxes/"+sb.ID, nil) })
	c.waitStatus(sb.ID, "running", 5*time.Minute)

	var term struct {
		WSURL string `json:"ws_url"`
		Token string `json:"token"`
	}
	c.json("GET", "/api/workspaces/"+sb.ID+"/terminal", nil, 200, &term)
	url := strings.Replace(term.WSURL, "http", "ws", 1) + "?token=" + term.Token
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("terminal websocket: %v (%+v)", err, term)
	}
	defer conn.Close()
	conn.WriteJSON(map[string]any{"type": "pty_spawn", "cols": 80, "rows": 24})
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		_, m, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("the terminal never became ready: %v", err)
		}
		if strings.Contains(string(m), "pty_ready") {
			break
		}
	}
	conn.SetReadDeadline(time.Time{})

	got := make(chan string, 100)
	go func() {
		for {
			_, m, err := conn.ReadMessage()
			if err != nil {
				close(got)
				return
			}
			got <- string(m)
		}
	}()
	res := c.exec(sb.ID, "sh", "-c", "echo TOPSECRET-FROM-EXEC")
	out := res.Stdout
	if !strings.Contains(out, "TOPSECRET-FROM-EXEC") {
		t.Fatalf("the API did not return the command's output: %q", out)
	}
	deadline := time.After(4 * time.Second)
	n := 0
	for {
		select {
		case m, ok := <-got:
			if !ok {
				t.Logf("saw %d terminal messages, none with the exec output", n)
				return
			}
			n++
			if strings.Contains(m, "exec_res") || strings.Contains(m, "VE9QU0VDUkVU") {
				t.Fatalf("the terminal received the command's result: %s", m)
			}
		case <-deadline:
			t.Logf("saw %d terminal messages, none with the exec output", n)
			return
		}
	}
}
