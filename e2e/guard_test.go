//go:build e2e

package e2e

import (
	"encoding/base64"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Needs the egress gateway installed (helm: guard.enabled=true, with a gateway secret named
// UPSTREAM_KEY whose value is the key in e2e/testdata/upstream.yaml) and the stand-in
// internal service applied (kubectl apply -f e2e/testdata/upstream.yaml). Skipped unless
// IGLOO_E2E_GUARD=1.

const upstreamSecret = "real-upstream-secret-9f3a" // what the stand-in service expects; the workspace must never see it

var upstreamHost = env("IGLOO_E2E_UPSTREAM_HOST", "internal-api.default.svc.cluster.local")

// shell is one terminal session into a workspace that runs commands and returns their output.
type shell struct {
	t    *testing.T
	conn *websocket.Conn
}

func (c *client) shell(id string) *shell {
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
	c.t.Cleanup(func() { conn.Close() })
	conn.WriteJSON(map[string]any{"type": "pty_spawn", "cols": 200, "rows": 50})
	sh := &shell{t: c.t, conn: conn}
	sh.read("", 30*time.Second, true) // wait for pty_ready
	return sh
}

// read collects terminal output until it contains want (or, with ready, until pty_ready).
func (s *shell) read(want string, within time.Duration, ready bool) string {
	var out strings.Builder
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		s.conn.SetReadDeadline(time.Now().Add(within))
		var m struct{ Type, Data string }
		if err := s.conn.ReadJSON(&m); err != nil {
			s.t.Fatalf("terminal read (so far %q): %v", out.String(), err)
		}
		switch m.Type {
		case "pty_ready":
			if ready {
				return ""
			}
		case "pty_output":
			b, _ := base64.StdEncoding.DecodeString(m.Data)
			out.Write(b)
		}
		if want != "" && strings.Contains(out.String(), want) {
			return out.String()
		}
	}
	s.t.Fatalf("timed out waiting for %q; got %q", want, out.String())
	return ""
}

// run executes cmd and returns what it printed. The end marker is computed by the shell
// ($((40+2))), so it cannot be mistaken for the echo of what was typed.
func (s *shell) run(cmd string, within time.Duration) string {
	s.t.Helper()
	line := cmd + "; echo __END$((40+2))__\n"
	s.conn.WriteJSON(map[string]any{"type": "pty_data", "data": base64.StdEncoding.EncodeToString([]byte(line))})
	return s.read("__END42__", within, false)
}

var codeRe = regexp.MustCompile(`CODE=(\d{3})`)

// httpCode runs curl with extra flags against url and returns its HTTP status ("000" when the
// connection never completed).
func (s *shell) httpCode(flags, url string) string {
	s.t.Helper()
	out := s.run(`printf 'CODE=%s\n' "$(curl -s -m 8 -o /dev/null -w '%{http_code}' `+flags+` `+url+`)"`, 40*time.Second)
	m := codeRe.FindStringSubmatch(out)
	if m == nil {
		s.t.Fatalf("no status in output %q", out)
	}
	return m[1]
}

func TestGuardedWorkspace(t *testing.T) {
	if os.Getenv("IGLOO_E2E_GUARD") != "1" {
		t.Skip("set IGLOO_E2E_GUARD=1 (needs the gateway and e2e/testdata/upstream.yaml installed)")
	}
	email, pw := os.Getenv("IGLOO_E2E_EMAIL"), os.Getenv("IGLOO_E2E_PASSWORD")
	if email == "" || pw == "" {
		t.Skip("set IGLOO_E2E_EMAIL and IGLOO_E2E_PASSWORD")
	}
	c := &client{t: t}
	var login struct{ Token string }
	c.json("POST", "/api/auth/login", map[string]string{"email": email, "password": pw}, 200, &login)
	c.token = login.Token

	policy := map[string]any{
		"allow":         []string{upstreamHost},
		"allow_private": true, // the stand-in service is inside the cluster
		"routes": []map[string]string{{
			"name": "internal", "upstream": "http://" + upstreamHost + ":8080", "header": "X-Api-Key", "secret": "UPSTREAM_KEY",
		}},
	}
	var ws struct{ ID string }
	c.json("POST", "/api/workspaces", map[string]any{
		"name": "e2e-guard", "template_id": template, "vars": map[string]string{"workspace_image": image}, "guard": policy,
	}, 201, &ws)
	t.Logf("created guarded workspace %s", ws.ID)
	t.Cleanup(func() { c.do("DELETE", "/api/workspaces/"+ws.ID, nil) })
	c.waitStatus(ws.ID, "running", 5*time.Minute)

	sh := c.shell(ws.ID)
	cc := func(t *testing.T) *client { return &client{t: t, token: c.token} }

	t.Run("an allowed internal host is reachable through the gateway", func(t *testing.T) {
		sh.t = t
		// 401 is the stand-in service answering "no key": it was reached. Without the gateway
		// this would hang, because the network policy lets the workspace reach nothing else.
		if got := sh.httpCode("", "http://"+upstreamHost+":8080/"); got != "401" {
			t.Errorf("allowed host: HTTP %s, want 401 from the service", got)
		}
	})

	t.Run("a host outside the allow-list is refused by the gateway", func(t *testing.T) {
		sh.t = t
		if got := sh.httpCode("", "http://not-allowed.example.com/"); got != "403" {
			t.Errorf("host outside the allow-list: HTTP %s, want 403", got)
		}
	})

	t.Run("going around the proxy is blocked by the network", func(t *testing.T) {
		sh.t = t
		for _, target := range []string{"http://" + upstreamHost + ":8080/", "http://1.1.1.1/"} {
			if got := sh.httpCode("--noproxy '*' --connect-timeout 4", target); got != "000" {
				t.Errorf("direct connection to %s got HTTP %s; it must not connect at all", target, got)
			}
		}
	})

	t.Run("the gateway adds the credential and the workspace never holds it", func(t *testing.T) {
		sh.t = t
		// The workspace presents only its placeholder token; the service answers 200 only for the real key.
		got := sh.httpCode(`-H "Authorization: Bearer $IGLOO_ROUTE_TOKEN"`, `"$IGLOO_ROUTE_BASE/internal/anything"`)
		if got != "200" {
			t.Errorf("route call: HTTP %s, want 200 (credential injected)", got)
		}
		leak := sh.run(`env | grep -c '`+upstreamSecret+`'; echo; cat /proc/self/environ | tr '\0' '\n' | grep -c '`+upstreamSecret+`'`, 20*time.Second)
		if regexp.MustCompile(`(?m)^[1-9]\d*\s*$`).MatchString(strings.ReplaceAll(leak, "\r", "")) {
			t.Errorf("the workspace's environment contains the upstream secret: %q", leak)
		}
	})

	t.Run("activity shows what happened", func(t *testing.T) {
		var evs []struct {
			Kind, Decision, Host, Rule string
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			cc(t).json("GET", "/api/workspaces/"+ws.ID+"/activity?limit=200", nil, 200, &evs)
			var denied, routed bool
			for _, e := range evs {
				denied = denied || (e.Decision == "deny" && strings.Contains(e.Host, "not-allowed.example.com"))
				routed = routed || (e.Kind == "route" && e.Decision == "allow" && e.Rule == "route:internal")
			}
			if denied && routed {
				return
			}
			time.Sleep(time.Second)
		}
		t.Errorf("activity is missing the refused host or the route call: %+v", evs)
	})

	t.Run("a policy change applies without a restart", func(t *testing.T) {
		sh.t = t
		cc(t).json("PUT", "/api/workspaces/"+ws.ID+"/guard-policy", map[string]any{"allow": []string{}, "allow_private": true}, 200, nil)
		// The gateway re-reads policies every ~15 s.
		deadline := time.Now().Add(45 * time.Second)
		got := ""
		for time.Now().Before(deadline) {
			if got = sh.httpCode("", "http://"+upstreamHost+":8080/"); got == "403" {
				return
			}
			time.Sleep(3 * time.Second)
		}
		t.Errorf("after removing the host from the allow-list it still answered HTTP %s, want 403", got)
	})
}
