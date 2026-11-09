package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/guard"
	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/relay"
)

type sandboxRig struct {
	h     *Handler
	srv   *httptest.Server
	alice string
	bob   string

	mu    sync.Mutex
	files map[string][]byte
	calls []agentMsg
	mute  bool // when true the fake agent never answers
}

func newSandboxRig(t *testing.T) *sandboxRig {
	h := newTestHandler()
	h.Relay = relay.New()
	h.GuardKey, h.GuardProxyURL = "gk", "http://igloo-guard.test:3128"
	g := &sandboxRig{h: h, files: map[string][]byte{}}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	r := chi.NewRouter()
	r.Get("/agent/{id}", func(w http.ResponseWriter, req *http.Request) {
		c, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		h.Relay.RegisterAgent(chi.URLParam(req, "id"), "a", c)
	})
	r.Group(func(r chi.Router) {
		r.Use(h.RequireAuth)
		r.Post("/api/sandboxes", h.CreateSandbox)
		r.Delete("/api/sandboxes/{id}", h.DeleteWorkspace)
		r.Post("/api/sandboxes/{id}/exec", h.SandboxExec)
		r.Get("/api/sandboxes/{id}/files", h.SandboxReadFile)
		r.Put("/api/sandboxes/{id}/files", h.SandboxWriteFile)
	})
	g.srv = httptest.NewServer(r)
	t.Cleanup(g.srv.Close)
	g.alice, _ = h.Auth.SignUser("alice", "a@x.dev")
	g.bob, _ = h.Auth.SignUser("bob", "b@x.dev")
	now := time.Now()
	h.Store.UpsertTemplate(context.Background(), &models.Template{ID: "tpl-kubernetes", Name: "k", Builtin: true, Provider: "kubernetes", CreatedAt: now, UpdatedAt: now})
	return g
}

func (g *sandboxRig) do(method, path, bearer string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	switch b := body.(type) {
	case nil:
	case []byte:
		buf.Write(b)
	default:
		json.NewEncoder(&buf).Encode(b)
	}
	req := httptest.NewRequest(method, path, &buf)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	rt := g.srv.Config.Handler
	rt.ServeHTTP(rec, req)
	return rec
}

// sandbox creates one (owned by alice) and attaches a fake agent for it unless noAgent.
func (g *sandboxRig) sandbox(t *testing.T, noAgent bool) string {
	rec := g.do("POST", "/api/sandboxes", g.alice, map[string]any{"name": "t"})
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var out struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if !noAgent {
		g.attachAgent(t, out.ID)
	}
	return out.ID
}

func (g *sandboxRig) attachAgent(t *testing.T, wsID string) {
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(g.srv.URL, "http")+"/agent/"+wsID, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		for {
			var m agentMsg
			if err := c.ReadJSON(&m); err != nil {
				return
			}
			g.mu.Lock()
			g.calls = append(g.calls, m)
			mute := g.mute
			g.mu.Unlock()
			if mute {
				continue
			}
			switch m.Type {
			case "exec":
				in, _ := base64.StdEncoding.DecodeString(m.Data)
				out := strings.Join(m.Cmd, " ") + "|stdin=" + string(in)
				res := agentMsg{Type: "exec_res", ID: m.ID, Exit: 7, TimedOut: m.Cmd[0] == "sleep",
					Stdout: base64.StdEncoding.EncodeToString([]byte(out)), Stderr: base64.StdEncoding.EncodeToString([]byte("warn"))}
				if m.Cmd[0] == "nope" {
					res = agentMsg{Type: "exec_res", ID: m.ID, Exit: -1, Err: "no such program"}
				}
				c.WriteJSON(res)
			case "fs_write":
				d, _ := base64.StdEncoding.DecodeString(m.Data)
				g.mu.Lock()
				g.files[m.Path] = d
				g.mu.Unlock()
				if strings.HasPrefix(m.Path, "/etc") {
					c.WriteJSON(agentMsg{Type: "fs_res", ID: m.ID, Err: "path \"" + m.Path + "\" is outside the workspace directory"})
					continue
				}
				c.WriteJSON(agentMsg{Type: "fs_res", ID: m.ID})
			case "fs_read":
				g.mu.Lock()
				d, ok := g.files[m.Path]
				g.mu.Unlock()
				if !ok {
					c.WriteJSON(agentMsg{Type: "fs_res", ID: m.ID, Err: "no such file"})
					continue
				}
				c.WriteJSON(agentMsg{Type: "fs_res", ID: m.ID, Data: base64.StdEncoding.EncodeToString(d)})
			}
		}
	}()
	for i := 0; i < 200 && !g.h.Relay.AgentConnected(wsID); i++ {
		time.Sleep(10 * time.Millisecond)
	}
}

// ─── creating ─────────────────────────────────────────────────────────────────

func TestASandboxIsCreatedBehindTheGatewayWithAnEmptyPolicyAndAnExpiry(t *testing.T) {
	g := newSandboxRig(t)
	id := g.sandbox(t, true)
	ws, _ := g.h.Store.GetWorkspace(context.Background(), id)

	if ws.Meta[metaSandbox] != "true" {
		t.Error("not marked as a sandbox")
	}
	pol, guarded := policyOf(ws)
	if !guarded || len(pol.Allow) != 0 || len(pol.Routes) != 0 || pol.AllowPrivate {
		t.Errorf("default policy = %+v (guarded=%v); a sandbox must start able to reach nothing", pol, guarded)
	}
	exp, err := time.Parse(time.RFC3339, ws.Meta[metaSandboxExpires])
	if err != nil || time.Until(exp) < 59*time.Minute || time.Until(exp) > 61*time.Minute {
		t.Errorf("expiry = %v (%v), want about an hour from now", exp, err)
	}
	if ws.TemplateID != "tpl-kubernetes" || ws.UserID != "alice" {
		t.Errorf("template %q owner %q", ws.TemplateID, ws.UserID)
	}
}

func TestSandboxCreationRefusesToSilentlyRunUnguarded(t *testing.T) {
	g := newSandboxRig(t)
	g.h.GuardProxyURL = "" // gateway not configured
	rec := g.do("POST", "/api/sandboxes", g.alice, map[string]any{})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "unguarded") {
		t.Errorf("no gateway and no opt-out: %d %s; want 409 explaining how to proceed", rec.Code, rec.Body)
	}
	rec = g.do("POST", "/api/sandboxes", g.alice, map[string]any{"unguarded": true})
	if rec.Code != 201 {
		t.Fatalf("explicit opt-out: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		ID      string
		Guarded bool
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	ws, _ := g.h.Store.GetWorkspace(context.Background(), out.ID)
	if _, guarded := policyOf(ws); guarded || out.Guarded {
		t.Error("an opted-out sandbox must not be marked guarded")
	}
	if rec := g.do("POST", "/api/sandboxes", g.alice, map[string]any{"unguarded": true, "guard": map[string]any{}}); rec.Code != 400 {
		t.Errorf("unguarded together with a policy: %d, want 400", rec.Code)
	}
}

func TestSandboxLifetimeIsBounded(t *testing.T) {
	g := newSandboxRig(t)
	for secs, want := range map[int]int{30: 400, 59: 400, 60: 201, 3600: 201, 86400: 201, 86401: 400, 604800: 400, -5: 400} {
		if got := g.do("POST", "/api/sandboxes", g.alice, map[string]any{"ttl_seconds": secs}).Code; got != want {
			t.Errorf("ttl_seconds=%d: %d, want %d", secs, got, want)
		}
	}
}

func TestSandboxCreationHonoursAPolicyAndAnImage(t *testing.T) {
	g := newSandboxRig(t)
	rec := g.do("POST", "/api/sandboxes", g.alice, map[string]any{
		"image": "my/image:1",
		"guard": guard.Policy{Allow: []string{"api.example.com"}, Routes: []guard.Route{{Name: "llm", Upstream: "https://api.example.com", Secret: "KEY"}}},
	})
	if rec.Code != 201 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct{ ID string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	ws, _ := g.h.Store.GetWorkspace(context.Background(), out.ID)
	pol, _ := policyOf(ws)
	if len(pol.Allow) != 1 || pol.Routes[0].Secret != "KEY" || ws.Vars["workspace_image"] != "my/image:1" {
		t.Errorf("policy %+v vars %v", pol, ws.Vars)
	}
	if rec := g.do("POST", "/api/sandboxes", g.alice, map[string]any{"guard": map[string]any{"allow": []string{"*"}}}); rec.Code != 400 {
		t.Errorf("invalid policy: %d, want 400", rec.Code)
	}
}

// ─── running things ───────────────────────────────────────────────────────────

func TestExecRoundTrip(t *testing.T) {
	g := newSandboxRig(t)
	id := g.sandbox(t, false)
	rec := g.do("POST", "/api/sandboxes/"+id+"/exec", g.alice, map[string]any{"cmd": []string{"echo", "hi"}, "stdin": "data", "timeout_s": 5})
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		ExitCode int `json:"exit_code"`
		Stdout   string
		Stderr   string
		TimedOut bool `json:"timed_out"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.ExitCode != 7 || out.Stdout != "echo hi|stdin=data" || out.Stderr != "warn" || out.TimedOut {
		t.Errorf("result = %+v", out)
	}
	g.mu.Lock()
	last := g.calls[len(g.calls)-1]
	g.mu.Unlock()
	if last.TimeoutS != 5 {
		t.Errorf("agent was told timeout %d, want 5", last.TimeoutS)
	}

	// The command's timeout is clamped before it reaches the agent.
	g.do("POST", "/api/sandboxes/"+id+"/exec", g.alice, map[string]any{"cmd": []string{"true"}, "timeout_s": 99999})
	g.mu.Lock()
	last = g.calls[len(g.calls)-1]
	g.mu.Unlock()
	if last.TimeoutS != 600 {
		t.Errorf("timeout passed on = %d, want it clamped to 600", last.TimeoutS)
	}

	if rec := g.do("POST", "/api/sandboxes/"+id+"/exec", g.alice, map[string]any{"cmd": []string{"sleep", "9"}}); !strings.Contains(rec.Body.String(), `"timed_out":true`) {
		t.Errorf("a timed-out command must say so: %s", rec.Body)
	}
	if rec := g.do("POST", "/api/sandboxes/"+id+"/exec", g.alice, map[string]any{"cmd": []string{"nope"}}); rec.Code != 400 || !strings.Contains(rec.Body.String(), "no such program") {
		t.Errorf("a command that cannot start: %d %s, want 400 with the reason", rec.Code, rec.Body)
	}
}

func TestExecValidationAndLimits(t *testing.T) {
	g := newSandboxRig(t)
	id := g.sandbox(t, false)
	for name, body := range map[string]map[string]any{
		"no command":         {"cmd": []string{}},
		"empty program":      {"cmd": []string{""}},
		"too many arguments": {"cmd": make([]string, maxExecArgs+1)},
		"huge command":       {"cmd": []string{strings.Repeat("x", maxExecBytes+1)}},
		"huge stdin":         {"cmd": []string{"cat"}, "stdin": strings.Repeat("x", maxExecStdin+1)},
	} {
		if name == "too many arguments" {
			for i := range body["cmd"].([]string) {
				body["cmd"].([]string)[i] = "a"
			}
		}
		if code := g.do("POST", "/api/sandboxes/"+id+"/exec", g.alice, body).Code; code != 400 && code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: %d, want 400 or 413", name, code)
		}
	}
}

func TestSandboxEndpointsAreForTheOwnerAndForSandboxesOnly(t *testing.T) {
	g := newSandboxRig(t)
	id := g.sandbox(t, false)
	plain := &models.Workspace{ID: "plain", UserID: "alice", Status: models.StatusRunning, Meta: map[string]string{}, CreatedAt: time.Now(), UpdatedAt: time.Now(), LastActiveAt: time.Now()}
	g.h.Store.CreateWorkspace(context.Background(), plain)

	exec := map[string]any{"cmd": []string{"true"}}
	for _, c := range []struct {
		name, who, path string
		want            int
	}{
		{"owner", g.alice, id, 200},
		{"another user", g.bob, id, 404},
		{"anonymous", "", id, 401},
		{"an ordinary workspace", g.alice, "plain", 404},
		{"unknown id", g.alice, "ghost", 404},
	} {
		if got := g.do("POST", "/api/sandboxes/"+c.path+"/exec", c.who, exec).Code; got != c.want {
			t.Errorf("exec as %s: %d, want %d", c.name, got, c.want)
		}
	}
	if got := g.do("PUT", "/api/sandboxes/"+id+"/files?path=x", g.bob, []byte("x")).Code; got != 404 {
		t.Errorf("another user writing a file: %d, want 404", got)
	}
}

func TestExecNeedsARunningSandboxAndGivesUpWhenTheAgentIsSilent(t *testing.T) {
	g := newSandboxRig(t)
	notRunning := g.sandbox(t, true)
	if got := g.do("POST", "/api/sandboxes/"+notRunning+"/exec", g.alice, map[string]any{"cmd": []string{"true"}}).Code; got != http.StatusConflict {
		t.Errorf("sandbox with no agent: %d, want 409", got)
	}

	old := agentReplyGrace
	agentReplyGrace = 150 * time.Millisecond
	defer func() { agentReplyGrace = old }()
	g.mute = true
	silent := g.sandbox(t, false)
	start := time.Now()
	rec := g.do("POST", "/api/sandboxes/"+silent+"/exec", g.alice, map[string]any{"cmd": []string{"true"}, "timeout_s": 1})
	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("silent agent: %d, want 504", rec.Code)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("waited %v for a command with a 1 s timeout", took)
	}
}

func TestFilesRoundTripAndErrorsComeBackWithTheirReason(t *testing.T) {
	g := newSandboxRig(t)
	id := g.sandbox(t, false)
	body := []byte("hello\x00binary\xff")
	if rec := g.do("PUT", "/api/sandboxes/"+id+"/files?path=dir/a.bin&mode=0755", g.alice, body); rec.Code != 204 {
		t.Fatalf("write: %d %s", rec.Code, rec.Body)
	}
	g.mu.Lock()
	var wrote agentMsg
	for _, c := range g.calls {
		if c.Type == "fs_write" {
			wrote = c
		}
	}
	g.mu.Unlock()
	if wrote.Path != "dir/a.bin" || wrote.Mode != 0o755 {
		t.Errorf("agent was asked to write %q with mode %o", wrote.Path, wrote.Mode)
	}
	rec := g.do("GET", "/api/sandboxes/"+id+"/files?path=dir/a.bin", g.alice, nil)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), body) {
		t.Errorf("read back %d %q, want the exact bytes", rec.Code, rec.Body.Bytes())
	}
	if rec := g.do("GET", "/api/sandboxes/"+id+"/files?path=missing", g.alice, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), "no such file") {
		t.Errorf("missing file: %d %s", rec.Code, rec.Body)
	}
	if rec := g.do("PUT", "/api/sandboxes/"+id+"/files?path=/etc/passwd", g.alice, []byte("x")); rec.Code != 400 || !strings.Contains(rec.Body.String(), "outside the workspace") {
		t.Errorf("agent's refusal must reach the caller: %d %s", rec.Code, rec.Body)
	}
	if got := g.do("GET", "/api/sandboxes/"+id+"/files", g.alice, nil).Code; got != 400 {
		t.Errorf("no path: %d, want 400", got)
	}
	if got := g.do("PUT", "/api/sandboxes/"+id+"/files?path=x&mode=9999", g.alice, []byte("x")).Code; got != 400 {
		t.Errorf("bad mode: %d, want 400", got)
	}
	if got := g.do("PUT", "/api/sandboxes/"+id+"/files?path=big", g.alice, make([]byte, maxFileBytes+1)).Code; got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized file: %d, want 413", got)
	}
}

// ─── expiry ───────────────────────────────────────────────────────────────────

func TestOnlyExpiredSandboxesAreDeleted(t *testing.T) {
	g := newSandboxRig(t)
	ctx := context.Background()
	now := time.Now()
	mk := func(id string, meta map[string]string) {
		g.h.Store.CreateWorkspace(ctx, &models.Workspace{ID: id, UserID: "alice", Provider: "kubernetes", Status: models.StatusRunning,
			Meta: meta, CreatedAt: now, UpdatedAt: now, LastActiveAt: now})
	}
	past, future := now.Add(-time.Minute).UTC().Format(time.RFC3339), now.Add(time.Hour).UTC().Format(time.RFC3339)
	mk("expired", map[string]string{metaSandbox: "true", metaSandboxExpires: past})
	mk("fresh", map[string]string{metaSandbox: "true", metaSandboxExpires: future})
	mk("no-expiry", map[string]string{metaSandbox: "true"})
	mk("garbled", map[string]string{metaSandbox: "true", metaSandboxExpires: "tomorrow-ish"})
	mk("ordinary", map[string]string{metaSandboxExpires: past}) // an expiry on something that is not a sandbox is ignored

	if n := g.h.expireSandboxes(ctx); n != 1 {
		t.Errorf("deleted %d sandboxes, want exactly the expired one", n)
	}
	for id, want := range map[string]models.WorkspaceStatus{
		"expired": models.StatusDeleting, "fresh": models.StatusRunning, "no-expiry": models.StatusRunning,
		"garbled": models.StatusRunning, "ordinary": models.StatusRunning,
	} {
		if ws, _ := g.h.Store.GetWorkspace(ctx, id); ws.Status != want {
			t.Errorf("%s is %s, want %s", id, ws.Status, want)
		}
	}
	if n := g.h.expireSandboxes(ctx); n != 0 {
		t.Errorf("a second pass deleted %d more; a sandbox already being deleted must be left alone", n)
	}
	// A destroy job was queued for the expired one.
	if j, _ := g.h.Store.ClaimNextJob(ctx, "p"); j == nil || j.WorkspaceID != "expired" || j.Action != models.JobDestroy {
		t.Errorf("claimed %+v, want a destroy job for the expired sandbox", j)
	}
}

// A guarded sandbox can't install anything while it starts (the gateway refuses package
// mirrors), so an image without curl would sit in "creating" forever. The server therefore
// picks a configured image unless the caller names one.
func TestSandboxImageDefaultsToTheConfiguredOneAndTheCallersChoiceWins(t *testing.T) {
	g := newSandboxRig(t)
	image := func(body map[string]any) (string, bool) {
		rec := g.do("POST", "/api/sandboxes", g.alice, body)
		if rec.Code != 201 {
			t.Fatalf("create: %d %s", rec.Code, rec.Body)
		}
		var out struct{ ID string }
		json.Unmarshal(rec.Body.Bytes(), &out)
		ws, _ := g.h.Store.GetWorkspace(context.Background(), out.ID)
		v, set := ws.Vars["workspace_image"]
		return v, set
	}
	if _, set := image(map[string]any{}); set {
		t.Error("with no image configured and none requested, the template's own default should apply")
	}
	g.h.SandboxImage = "registry.example/igloo-workspace:1"
	if v, _ := image(map[string]any{}); v != "registry.example/igloo-workspace:1" {
		t.Errorf("default sandbox image = %q", v)
	}
	if v, _ := image(map[string]any{"image": "my/own:2"}); v != "my/own:2" {
		t.Errorf("a requested image must win, got %q", v)
	}
}
