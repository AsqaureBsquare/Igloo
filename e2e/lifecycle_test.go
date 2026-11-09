//go:build e2e

// Package e2e drives a running Igloo through a real workspace lifecycle using the
// same endpoints and protocols the UI and CLI use.
//
//	IGLOO_E2E_EMAIL=you@example.com IGLOO_E2E_PASSWORD=... \
//	  go test -mod=vendor -tags e2e -count=1 -timeout 20m -v ./e2e
//
// Optional: IGLOO_URL (default http://localhost:8080), IGLOO_E2E_TEMPLATE
// (default tpl-kubernetes), IGLOO_E2E_IMAGE (default igloo-workspace:dev).
// It creates one workspace, named e2e-<unix time>, and deletes it at the end.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var (
	base     = env("IGLOO_URL", "http://localhost:8080")
	template = env("IGLOO_E2E_TEMPLATE", "tpl-kubernetes")
	image    = env("IGLOO_E2E_IMAGE", "igloo-workspace:dev")
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type client struct {
	t     *testing.T
	token string
}

func (c *client) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (c *client) json(method, path string, body any, want int, out any) {
	c.t.Helper()
	code, raw := c.do(method, path, body)
	if code != want {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, code, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: bad JSON %q: %v", method, path, raw, err)
		}
	}
}

func (c *client) status(id string) string {
	_, raw := c.do("GET", "/api/workspaces/"+id, nil)
	var w struct{ Status string }
	json.Unmarshal(raw, &w)
	return w.Status
}

func (c *client) waitStatus(id, want string, within time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(within)
	last := ""
	for time.Now().Before(deadline) {
		if s := c.status(id); s != last {
			c.t.Logf("workspace %s: %s", id[:8], s)
			last = s
		}
		if last == want {
			return
		}
		if last == "error" {
			c.t.Fatalf("workspace went to error while waiting for %q", want)
		}
		time.Sleep(3 * time.Second)
	}
	c.t.Fatalf("workspace still %q after %v, wanted %q", last, within, want)
}

func wsURL(u string) string { return "ws" + strings.TrimPrefix(u, "http") }

// terminal opens the browser-style PTY session and runs a command whose output
// can't be confused with its own echo.
func (c *client) terminal(id string) {
	c.t.Helper()
	var tt struct {
		Token string `json:"token"`
		WSURL string `json:"ws_url"`
	}
	c.json("GET", "/api/workspaces/"+id+"/terminal", nil, 200, &tt)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(tt.WSURL)+"?token="+tt.Token, nil)
	if err != nil {
		c.t.Fatalf("terminal dial: %v", err)
	}
	defer conn.Close()
	conn.WriteJSON(map[string]any{"type": "pty_spawn", "cols": 100, "rows": 30})
	send := func(s string) {
		conn.WriteJSON(map[string]any{"type": "pty_data", "data": base64.StdEncoding.EncodeToString([]byte(s))})
	}
	ready := false
	var out strings.Builder
	deadline := time.Now().Add(40 * time.Second)
	sent := false
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var m struct{ Type, Data string }
		if err := conn.ReadJSON(&m); err != nil {
			c.t.Fatalf("terminal read (ready=%v, output so far %q): %v", ready, out.String(), err)
		}
		switch m.Type {
		case "pty_ready":
			ready = true
		case "pty_output":
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			out.Write(b)
		case "pty_error":
			c.t.Fatalf("terminal error: %s", m.Data)
		}
		if ready && !sent {
			send("echo igloo-e2e-$((6*7))\n")
			sent = true
		}
		if strings.Contains(out.String(), "igloo-e2e-42") {
			return
		}
	}
	c.t.Fatalf("command output never arrived; got %q", out.String())
}

// oversizedRefused sends a terminal message far larger than any real keystroke or
// paste. The server must close that connection for size (status 1009) and keep
// serving everyone else.
func (c *client) oversizedRefused(id string) {
	c.t.Helper()
	var tt struct {
		Token string `json:"token"`
		WSURL string `json:"ws_url"`
	}
	c.json("GET", "/api/workspaces/"+id+"/terminal", nil, 200, &tt)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(tt.WSURL)+"?token="+tt.Token, nil)
	if err != nil {
		c.t.Fatalf("terminal dial: %v", err)
	}
	defer conn.Close()
	conn.WriteMessage(websocket.TextMessage, bytes.Repeat([]byte("A"), 5<<20))
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			if ce, ok := err.(*websocket.CloseError); !ok || ce.Code != websocket.CloseMessageTooBig {
				c.t.Fatalf("oversized message ended the connection with %v, want a 1009 close", err)
			}
			break
		}
	}
	// The server closes the socket without reading the rest of the message, which resets
	// the TCP connection. `kubectl port-forward` exits on any reset (its log says "lost
	// connection to pod"), so give the forward a moment to come back before asking
	// whether the server is still serving.
	c.waitHealthy(30 * time.Second)
	c.terminal(id) // a normal session still works
}

// waitHealthy polls /health, tolerating connection errors, until it answers 200.
func (c *client) waitHealthy(within time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(within)
	hc := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		if resp, err := hc.Get(base + "/health"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	c.t.Fatalf("server did not answer /health within %v", within)
}

// forward opens a raw TCP tunnel to code-server (:13131) with the credential in a
// header, as the CLI does, and speaks HTTP over it. A workspace that reports
// "running" must already have VS Code reachable, so this makes a single attempt.
func (c *client) forward(id string) {
	c.t.Helper()
	var ft struct{ Token string }
	c.json("GET", "/api/workspaces/"+id+"/forward-token", nil, 200, &ft)
	hdr := http.Header{"Authorization": {"Bearer " + ft.Token}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(base)+"/api/workspaces/"+id+"/forward?port=13131", hdr)
	if err != nil {
		c.t.Fatalf("forward dial: %v", err)
	}
	defer conn.Close()
	conn.WriteMessage(websocket.BinaryMessage, []byte("GET /healthz HTTP/1.0\r\nHost: localhost\r\n\r\n"))
	var got []byte
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for !bytes.Contains(got, []byte("HTTP/1.")) {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			c.t.Fatalf("tunnel to code-server failed the moment the workspace was running: %v (got %q)", err, got)
		}
		got = append(got, msg...)
	}
	c.t.Logf("code-server answered through the tunnel on the first attempt: %.40q", got)
}

// vscode follows the one-time proxy URL the UI opens and expects code-server's page.
func (c *client) vscode(id string) {
	c.t.Helper()
	var vt struct{ URL string }
	c.json("GET", "/api/workspaces/"+id+"/vscode-token", nil, 200, &vt)
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	resp, err := hc.Get(base + vt.URL)
	if err != nil {
		c.t.Fatalf("vscode proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		c.t.Fatalf("vscode proxy: status %d: %.200s", resp.StatusCode, body)
	}
	c.t.Logf("VS Code page served (%d bytes)", len(body))
}

// provisionLog reads the live build log the way the UI's log tab does.
func (c *client) provisionLog(id string) {
	c.t.Helper()
	var tk struct{ Ticket string }
	c.json("GET", "/api/workspaces/"+id+"/logs-ticket", nil, 200, &tk)
	hc := &http.Client{Timeout: 12 * time.Second}
	resp, err := hc.Get(base + "/api/workspaces/" + id + "/provision-logs?ticket=" + tk.Ticket)
	if err != nil && resp == nil {
		c.t.Fatalf("provision-logs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		c.t.Fatalf("provision-logs: status %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	lines := 0
	for sc.Scan() && lines < 3 {
		if strings.HasPrefix(sc.Text(), "data:") {
			lines++
		}
	}
	if lines == 0 {
		c.t.Fatal("log stream returned no lines")
	}
	c.t.Logf("log stream delivered %d+ lines", lines)
}

func TestWorkspaceLifecycle(t *testing.T) {
	email, pw := os.Getenv("IGLOO_E2E_EMAIL"), os.Getenv("IGLOO_E2E_PASSWORD")
	if email == "" || pw == "" {
		t.Skip("set IGLOO_E2E_EMAIL and IGLOO_E2E_PASSWORD")
	}
	c := &client{t: t}

	if code, _ := c.do("GET", "/health", nil); code != 200 {
		t.Fatalf("health: %d", code)
	}
	var login struct{ Token string }
	c.json("POST", "/api/auth/login", map[string]string{"email": email, "password": pw}, 200, &login)
	c.token = login.Token

	var tpls []struct{ ID string }
	c.json("GET", "/api/templates", nil, 200, &tpls)
	found := false
	for _, x := range tpls {
		found = found || x.ID == template
	}
	if !found {
		t.Fatalf("template %q not offered: %v", template, tpls)
	}

	var ws struct{ ID string }
	name := fmt.Sprintf("e2e-%d", time.Now().Unix())
	c.json("POST", "/api/workspaces", map[string]any{
		"name": name, "template_id": template, "vars": map[string]string{"workspace_image": image},
	}, 201, &ws)
	t.Logf("created %s (%s)", name, ws.ID)
	deleted := false
	t.Cleanup(func() {
		if !deleted {
			c.do("DELETE", "/api/workspaces/"+ws.ID, nil)
		}
	})

	// The build log is only live while Terraform runs.
	t.Run("build log streams", func(t *testing.T) { (&client{t: t, token: c.token}).provisionLog(ws.ID) })

	c.waitStatus(ws.ID, "running", 5*time.Minute)
	cc := func(t *testing.T) *client { return &client{t: t, token: c.token} }

	t.Run("listed for its owner", func(t *testing.T) {
		var list []struct{ ID string }
		cc(t).json("GET", "/api/workspaces", nil, 200, &list)
		for _, w := range list {
			if w.ID == ws.ID {
				return
			}
		}
		t.Fatal("workspace missing from GET /api/workspaces")
	})
	t.Run("terminal runs a command", func(t *testing.T) { cc(t).terminal(ws.ID) })
	t.Run("oversized terminal message is refused", func(t *testing.T) { cc(t).oversizedRefused(ws.ID) })
	t.Run("port-forward reaches code-server", func(t *testing.T) { cc(t).forward(ws.ID) })
	t.Run("VS Code is served through the proxy", func(t *testing.T) { cc(t).vscode(ws.ID) })
	t.Run("agent logs readable", func(t *testing.T) {
		code, body := cc(t).do("GET", "/api/workspaces/"+ws.ID+"/agent-logs", nil)
		if code != 200 || len(bytes.TrimSpace(body)) == 0 {
			t.Fatalf("agent-logs: %d %q", code, body)
		}
	})

	t.Run("stop then start", func(t *testing.T) {
		k := cc(t)
		k.json("POST", "/api/workspaces/"+ws.ID+"/stop", nil, 200, nil)
		k.waitStatus(ws.ID, "sleeping", 3*time.Minute)
		k.json("POST", "/api/workspaces/"+ws.ID+"/start", nil, 200, nil)
		k.waitStatus(ws.ID, "running", 5*time.Minute)
		k.terminal(ws.ID) // the agent has to reconnect after a restart
	})

	t.Run("delete", func(t *testing.T) {
		k := cc(t)
		code, body := k.do("DELETE", "/api/workspaces/"+ws.ID, nil)
		if code != 200 && code != 202 && code != 204 {
			t.Fatalf("delete: %d %s", code, body)
		}
		deleted = true
		deadline := time.Now().Add(4 * time.Minute)
		for time.Now().Before(deadline) {
			if code, _ := k.do("GET", "/api/workspaces/"+ws.ID, nil); code == 404 {
				return
			}
			time.Sleep(3 * time.Second)
		}
		t.Fatal("workspace still present 4 minutes after delete")
	})
}
