//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// The sandbox API against a live cluster. Same requirements as guard_test.go
// (IGLOO_E2E_GUARD=1, gateway installed, e2e/testdata/upstream.yaml applied).

func (c *client) putFile(sandbox, path string, data []byte) int {
	c.t.Helper()
	req, _ := http.NewRequest("PUT", base+"/api/sandboxes/"+sandbox+"/files?path="+url.QueryEscape(path), bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("PUT file: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (c *client) getFile(sandbox, path string) (int, []byte) {
	c.t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/sandboxes/"+sandbox+"/files?path="+url.QueryEscape(path), nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("GET file: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

type execOut struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
}

func (c *client) exec(sandbox string, cmd ...string) execOut {
	c.t.Helper()
	var out execOut
	c.json("POST", "/api/sandboxes/"+sandbox+"/exec", map[string]any{"cmd": cmd, "timeout_s": 40}, 200, &out)
	return out
}

func guardedLogin(t *testing.T) *client {
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
	return c
}

func TestSandboxAPI(t *testing.T) {
	c := guardedLogin(t)
	var sb struct {
		ID        string `json:"id"`
		ExpiresAt string `json:"expires_at"`
		Guarded   bool   `json:"guarded"`
	}
	c.json("POST", "/api/sandboxes", map[string]any{"name": "e2e-sandbox", "ttl_seconds": 600, "image": image}, 201, &sb)
	t.Logf("created sandbox %s, expires %s", sb.ID, sb.ExpiresAt)
	t.Cleanup(func() { c.do("DELETE", "/api/sandboxes/"+sb.ID, nil) })
	if !sb.Guarded {
		t.Fatal("a sandbox created with no options must be guarded")
	}
	c.waitStatus(sb.ID, "running", 5*time.Minute)
	cc := func(t *testing.T) *client { return &client{t: t, token: c.token} }

	t.Run("run commands and read the results", func(t *testing.T) {
		k := cc(t)
		if r := k.exec(sb.ID, "sh", "-c", "echo hello; echo oops >&2; exit 4"); r.ExitCode != 4 || r.Stdout != "hello\n" || r.Stderr != "oops\n" {
			t.Errorf("result = %+v", r)
		}
		var r execOut
		k.json("POST", "/api/sandboxes/"+sb.ID+"/exec", map[string]any{"cmd": []string{"cat"}, "stdin": "from stdin"}, 200, &r)
		if r.Stdout != "from stdin" {
			t.Errorf("stdin not delivered: %+v", r)
		}
		k.json("POST", "/api/sandboxes/"+sb.ID+"/exec", map[string]any{"cmd": []string{"sleep", "30"}, "timeout_s": 2}, 200, &r)
		if !r.TimedOut {
			t.Errorf("a command past its timeout should say so: %+v", r)
		}
		if r := k.exec(sb.ID, "sh", "-c", "pgrep -f '[s]leep 30' | wc -l"); strings.TrimSpace(r.Stdout) != "0" {
			t.Errorf("the timed-out command left %s process(es) behind", strings.TrimSpace(r.Stdout))
		}
	})

	t.Run("files go in and come out unchanged and stay inside the workspace", func(t *testing.T) {
		k := cc(t)
		payload := []byte("line1\n\x00\xffbinary\n")
		if code := k.putFile(sb.ID, "work/data.bin", payload); code != 204 {
			t.Fatalf("write: HTTP %d", code)
		}
		if code, got := k.getFile(sb.ID, "work/data.bin"); code != 200 || !bytes.Equal(got, payload) {
			t.Errorf("read back HTTP %d %q", code, got)
		}
		if r := k.exec(sb.ID, "sh", "-c", "cat work/data.bin | wc -c"); strings.TrimSpace(r.Stdout) != "15" {
			t.Errorf("the file seen by a command has %s bytes, want 15", strings.TrimSpace(r.Stdout))
		}
		for _, p := range []string{"../../etc/passwd", "/etc/shadow", "/root/.ssh/x"} {
			if code := k.putFile(sb.ID, p, []byte("x")); code != 400 {
				t.Errorf("write to %q: HTTP %d, want 400 (outside the workspace)", p, code)
			}
			if code, _ := k.getFile(sb.ID, p); code != 400 {
				t.Errorf("read of %q: HTTP %d, want 400", p, code)
			}
		}
	})

	t.Run("a sandbox with no policy can reach nothing", func(t *testing.T) {
		k := cc(t)
		for _, target := range []string{"http://example.com/", "http://" + upstreamHost + ":8080/"} {
			r := k.exec(sb.ID, "sh", "-c", `printf 'CODE=%s' "$(curl -s -m 8 -o /dev/null -w '%{http_code}' `+target+`)"`)
			if m := codeRe.FindStringSubmatch(r.Stdout); m == nil || m[1] != "403" {
				t.Errorf("%s from a sandbox with an empty policy: %q, want HTTP 403 from the gateway", target, r.Stdout)
			}
		}
		r := k.exec(sb.ID, "sh", "-c", `printf 'CODE=%s' "$(curl -s -m 5 --noproxy '*' --connect-timeout 4 -o /dev/null -w '%{http_code}' http://1.1.1.1/)"`)
		if m := codeRe.FindStringSubmatch(r.Stdout); m == nil || m[1] != "000" {
			t.Errorf("bypassing the proxy: %q, want 000 (dropped by the network)", r.Stdout)
		}
	})

	t.Run("an owner can open the sandbox and see the record", func(t *testing.T) {
		// The gateway reports in batches about once a second.
		var evs []struct{ Decision, Host string }
		denied := 0
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && denied < 2; time.Sleep(time.Second) {
			cc(t).json("GET", "/api/workspaces/"+sb.ID+"/activity?limit=50", nil, 200, &evs)
			denied = 0
			for _, e := range evs {
				if e.Decision == "deny" {
					denied++
				}
			}
		}
		if denied < 2 {
			t.Errorf("activity lists %d refusals; the two refused requests above should be there: %+v", denied, evs)
		}
	})

	t.Run("a different user cannot touch it", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"cmd": []string{"id"}})
		req, _ := http.NewRequest("POST", base+"/api/sandboxes/"+sb.ID+"/exec", bytes.NewReader(raw))
		resp, err := http.DefaultClient.Do(req) // no credentials at all
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("anonymous exec: HTTP %d, want 401", resp.StatusCode)
		}
	})

	t.Run("delete", func(t *testing.T) {
		k := cc(t)
		if code, _ := k.do("DELETE", "/api/sandboxes/"+sb.ID, nil); code != 204 {
			t.Fatalf("delete: HTTP %d", code)
		}
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			if code, _ := k.do("GET", "/api/sandboxes/"+sb.ID, nil); code == 404 {
				return
			}
			time.Sleep(3 * time.Second)
		}
		t.Error("sandbox still present 3 minutes after delete")
	})
}

func TestSandboxDeletesItselfWhenItsTimeIsUp(t *testing.T) {
	c := guardedLogin(t)
	var sb struct{ ID string }
	c.json("POST", "/api/sandboxes", map[string]any{"name": "e2e-expiry", "ttl_seconds": 60, "image": image}, 201, &sb)
	t.Logf("created sandbox %s with a 60 s lifetime", sb.ID)
	t.Cleanup(func() { c.do("DELETE", "/api/sandboxes/"+sb.ID, nil) })

	// The janitor runs every 30 s, so deletion starts within about 90 s of creation.
	deadline := time.Now().Add(4 * time.Minute)
	sawDeleting := false
	for time.Now().Before(deadline) {
		code, raw := c.do("GET", "/api/sandboxes/"+sb.ID, nil)
		if code == 404 {
			return
		}
		var ws struct{ Status string }
		json.Unmarshal(raw, &ws)
		if ws.Status == "deleting" && !sawDeleting {
			sawDeleting = true
			t.Logf("expired: now deleting")
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatal("the sandbox was still there four minutes after a 60 s lifetime")
}
