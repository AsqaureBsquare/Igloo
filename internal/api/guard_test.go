package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/igloo-dev/igloo/internal/guard"
	"github.com/igloo-dev/igloo/internal/models"
)

const testGuardKey = "guard-key-for-tests"

type guardRig struct {
	h     *Handler
	rt    http.Handler
	alice string // bearer tokens
	bob   string
}

func newGuardRig(t *testing.T) *guardRig {
	h := newTestHandler()
	h.GuardKey = testGuardKey
	h.GuardProxyURL = "http://igloo-guard.test:3128"
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(h.GuardAuth)
		r.Post("/api/guard/authorize", h.GuardAuthorize)
		r.Post("/api/guard/events", h.GuardEvents)
	})
	r.Group(func(r chi.Router) {
		r.Use(h.RequireAuth)
		r.Post("/api/workspaces", h.CreateWorkspace)
		r.Get("/api/workspaces/{id}/activity", h.WorkspaceActivity)
		r.Get("/api/workspaces/{id}/guard-policy", h.GetGuardPolicy)
		r.Put("/api/workspaces/{id}/guard-policy", h.PutGuardPolicy)
	})
	alice, _ := h.Auth.SignUser("alice", "a@x.dev")
	bob, _ := h.Auth.SignUser("bob", "b@x.dev")
	now := time.Now()
	h.Store.UpsertTemplate(context.Background(), &models.Template{ID: "tpl", Name: "tpl", Builtin: true, Provider: "kubernetes", CreatedAt: now, UpdatedAt: now})
	return &guardRig{h: h, rt: r, alice: alice, bob: bob}
}

func (g *guardRig) do(method, path, bearer string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	g.rt.ServeHTTP(rec, req)
	return rec
}

// workspace stores a workspace owned by alice, guarded with pol (nil = not guarded).
func (g *guardRig) workspace(t *testing.T, id string, pol *guard.Policy) *models.Workspace {
	now := time.Now()
	ws := &models.Workspace{ID: id, UserID: "alice", Name: id, Status: models.StatusRunning, TemplateID: "tpl",
		Vars: map[string]string{"cpu": "1"}, Meta: map[string]string{}, CreatedAt: now, UpdatedAt: now, LastActiveAt: now}
	if pol != nil {
		setPolicy(ws, *pol)
	}
	if err := g.h.Store.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

func (g *guardRig) egressToken(wsID string) string {
	tok, _ := g.h.Auth.SignConn(wsID, guardTokenRole, time.Hour)
	return tok
}

// ─── what the provisioner is told ─────────────────────────────────────────────

func TestClaimVarsGiveAGuardedWorkspaceItsGatewayAndDropUserSuppliedGuardVars(t *testing.T) {
	g := newGuardRig(t)
	ws := g.workspace(t, "w1", &guard.Policy{Allow: []string{"a.example"}})
	// The user tries to opt out and to point the workspace at a proxy of their own.
	ws.Vars["guard_proxy"] = ""
	ws.Vars["guard_token"] = "mine"
	ws.Vars["guard_required"] = "false"
	ws.Vars["guard_anything"] = "x"

	v := g.h.claimVars(ws)
	if v["cpu"] != "1" {
		t.Error("ordinary variables must pass through")
	}
	if v["guard_required"] != "true" || v["guard_anything"] != "" {
		t.Errorf("user guard_* variables survived: %v", v)
	}
	u, err := url.Parse(v["guard_proxy"])
	if err != nil || u.Host != "igloo-guard.test:3128" || u.User == nil {
		t.Fatalf("guard_proxy = %q", v["guard_proxy"])
	}
	tok, _ := u.User.Password()
	claims, err := g.h.Auth.ParseConn(tok)
	if err != nil || claims.WorkspaceID != "w1" || claims.Role != guardTokenRole {
		t.Fatalf("the proxy credential is not an egress token for this workspace: %+v, %v", claims, err)
	}
	if v["guard_token"] != tok || v["guard_route_base"] != "http://igloo-guard.test:3128/route" {
		t.Errorf("route settings = %q %q", v["guard_token"], v["guard_route_base"])
	}
	// The stored workspace is untouched: tokens are minted per claim, not persisted.
	if _, stored := ws.Vars["guard_proxy"]; !stored || ws.Vars["guard_proxy"] != "" {
		t.Error("claimVars modified the stored variables")
	}
}

func TestAnUnguardedWorkspaceNeverCarriesGatewaySettings(t *testing.T) {
	g := newGuardRig(t)
	ws := g.workspace(t, "w1", nil)
	ws.Vars["guard_proxy"] = "http://evil.example"
	ws.Vars["guard_required"] = "true"
	v := g.h.claimVars(ws)
	for k := range v {
		if strings.HasPrefix(k, "guard_") {
			t.Errorf("unguarded workspace got %s=%q", k, v[k])
		}
	}
}

func TestAGuardedWorkspaceFailsClosedWhenTheGatewayIsNotConfigured(t *testing.T) {
	g := newGuardRig(t)
	ws := g.workspace(t, "w1", &guard.Policy{})
	g.h.GuardProxyURL = ""
	v := g.h.claimVars(ws)
	if v["guard_required"] != "true" || v["guard_proxy"] != "" {
		t.Errorf("vars = %v: must demand a gateway but supply none, so the template refuses to start", v)
	}
}

// ─── what the gateway may ask ─────────────────────────────────────────────────

func TestAuthorizeAnswersOnlyTheGatewayAndOnlyForEgressTokens(t *testing.T) {
	g := newGuardRig(t)
	pol := guard.Policy{Allow: []string{"api.example.com"}, Routes: []guard.Route{{Name: "llm", Upstream: "https://api.example.com", Secret: "KEY"}}}
	g.workspace(t, "w1", &pol)
	g.workspace(t, "plain", nil)
	agentTok, _ := g.h.Auth.SignConn("w1", "agent", time.Hour)

	post := func(key, token string) *httptest.ResponseRecorder {
		return g.do("POST", "/api/guard/authorize", key, map[string]string{"token": token})
	}
	rec := post(testGuardKey, g.egressToken("w1"))
	if rec.Code != 200 {
		t.Fatalf("valid egress token: %d %s", rec.Code, rec.Body)
	}
	var id guard.Identity
	json.Unmarshal(rec.Body.Bytes(), &id)
	if id.WorkspaceID != "w1" || len(id.Policy.Allow) != 1 || id.Policy.Routes[0].Secret != "KEY" {
		t.Errorf("identity = %+v", id)
	}
	if strings.Contains(rec.Body.String(), "sk-") {
		t.Error("the answer should name secrets, never carry them")
	}

	for name, c := range map[string]struct {
		key, token string
		want       int
	}{
		"no guard key":                     {"", g.egressToken("w1"), 401},
		"wrong guard key":                  {"nope", g.egressToken("w1"), 401},
		"a user session as guard key":      {g.alice, g.egressToken("w1"), 401},
		"agent token is not an egress one": {testGuardKey, agentTok, 401},
		"garbage token":                    {testGuardKey, "garbage", 401},
		"token for an unguarded workspace": {testGuardKey, g.egressToken("plain"), 401},
		"token for a missing workspace":    {testGuardKey, g.egressToken("ghost"), 401},
		"empty token":                      {testGuardKey, "", 400},
	} {
		if got := post(c.key, c.token).Code; got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}

	// A deleting workspace stops being served at once.
	ws, _ := g.h.Store.GetWorkspace(context.Background(), "w1")
	ws.Status = models.StatusDeleting
	g.h.Store.UpdateWorkspace(context.Background(), ws)
	if got := post(testGuardKey, g.egressToken("w1")).Code; got != 401 {
		t.Errorf("deleting workspace: %d, want 401", got)
	}

	// With no key configured at all, the endpoints are off.
	g.h.GuardKey = ""
	if got := post("", g.egressToken("w1")).Code; got != http.StatusServiceUnavailable {
		t.Errorf("gateway disabled: %d, want 503", got)
	}
}

func TestEventsAreStoredBoundedAndOnlyForRealWorkspaces(t *testing.T) {
	g := newGuardRig(t)
	g.workspace(t, "w1", &guard.Policy{})
	long := strings.Repeat("x", 5000)
	rec := g.do("POST", "/api/guard/events", testGuardKey, map[string]any{"events": []models.GuardEvent{
		{WorkspaceID: "w1", Kind: "connect", Decision: "deny", Host: long, Rule: "not-in-allow-list"},
		{WorkspaceID: "w1", Kind: "route", Decision: "allow", Host: "api.example.com", Path: "/v1/x", Status: 200, Time: time.Now().Add(time.Second)},
		{WorkspaceID: "someone-elses", Kind: "connect", Decision: "allow", Host: "junk"},
	}})
	if rec.Code != 204 {
		t.Fatalf("ingest: %d %s", rec.Code, rec.Body)
	}
	got := g.do("GET", "/api/workspaces/w1/activity", g.alice, nil)
	var evs []models.GuardEvent
	json.Unmarshal(got.Body.Bytes(), &evs)
	if len(evs) != 2 {
		t.Fatalf("stored %d events, want 2 (the unknown workspace's must be dropped): %+v", len(evs), evs)
	}
	for _, e := range evs {
		if len(e.Host) > maxGuardField {
			t.Errorf("host not clipped: %d bytes", len(e.Host))
		}
		if e.Time.IsZero() {
			t.Error("an event without a time should get one")
		}
	}
	if evs[0].Kind != "route" {
		t.Errorf("newest first expected, got %s then %s", evs[0].Kind, evs[1].Kind)
	}

	if got := g.do("POST", "/api/guard/events", "", map[string]any{"events": []models.GuardEvent{}}).Code; got != 401 {
		t.Errorf("no guard key: %d, want 401", got)
	}
	big := make([]models.GuardEvent, maxGuardEvents+1)
	if got := g.do("POST", "/api/guard/events", testGuardKey, map[string]any{"events": big}).Code; got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized batch: %d, want 413", got)
	}
}

// ─── what owners can see and change ───────────────────────────────────────────

func TestActivityAndPolicyBelongToTheWorkspaceOwner(t *testing.T) {
	g := newGuardRig(t)
	g.workspace(t, "w1", &guard.Policy{Allow: []string{"a.example"}})
	g.h.Store.(interface {
		AppendGuardEvents(context.Context, []models.GuardEvent) error
	}).AppendGuardEvents(context.Background(), []models.GuardEvent{{WorkspaceID: "w1", Kind: "connect", Decision: "deny", Host: "x:443", Time: time.Now()}})

	for _, c := range []struct{ method, path string }{
		{"GET", "/api/workspaces/w1/activity"}, {"GET", "/api/workspaces/w1/guard-policy"}, {"PUT", "/api/workspaces/w1/guard-policy"},
	} {
		if got := g.do(c.method, c.path, g.alice, guard.Policy{Allow: []string{"a.example"}}).Code; got != 200 {
			t.Errorf("owner %s %s: %d, want 200", c.method, c.path, got)
		}
		if got := g.do(c.method, c.path, g.bob, guard.Policy{}).Code; got != 404 {
			t.Errorf("another user %s %s: %d, want 404", c.method, c.path, got)
		}
		if got := g.do(c.method, c.path, "", nil).Code; got != 401 {
			t.Errorf("anonymous %s %s: %d, want 401", c.method, c.path, got)
		}
	}
}

func TestPolicyUpdatesAreValidatedAndOnlyForGuardedWorkspaces(t *testing.T) {
	g := newGuardRig(t)
	g.workspace(t, "guarded", &guard.Policy{Allow: []string{"a.example"}})
	g.workspace(t, "plain", nil)

	if got := g.do("PUT", "/api/workspaces/guarded/guard-policy", g.alice, guard.Policy{Allow: []string{"*"}}).Code; got != 400 {
		t.Errorf("bare wildcard: %d, want 400", got)
	}
	if got := g.do("PUT", "/api/workspaces/plain/guard-policy", g.alice, guard.Policy{Allow: []string{"a.example"}}).Code; got != 409 {
		t.Errorf("policy on an unguarded workspace: %d, want 409", got)
	}
	if got := g.do("PUT", "/api/workspaces/guarded/guard-policy", g.alice, guard.Policy{Allow: []string{"a.example", "b.example"}}).Code; got != 200 {
		t.Fatalf("valid update: %d", got)
	}
	// The gateway now sees the new policy.
	rec := g.do("POST", "/api/guard/authorize", testGuardKey, map[string]string{"token": g.egressToken("guarded")})
	var id guard.Identity
	json.Unmarshal(rec.Body.Bytes(), &id)
	if len(id.Policy.Allow) != 2 {
		t.Errorf("gateway sees %v, want the updated policy", id.Policy.Allow)
	}
}

func TestCreatingAGuardedWorkspace(t *testing.T) {
	g := newGuardRig(t)
	create := func(body map[string]any) *httptest.ResponseRecorder {
		return g.do("POST", "/api/workspaces", g.alice, body)
	}
	if got := create(map[string]any{"name": "a", "template_id": "tpl", "guard": map[string]any{"allow": []string{"*"}}}).Code; got != 400 {
		t.Errorf("invalid policy: %d, want 400", got)
	}
	g.h.GuardProxyURL = ""
	if got := create(map[string]any{"name": "a", "template_id": "tpl", "guard": map[string]any{"allow": []string{"a.example"}}}).Code; got != 409 {
		t.Errorf("no gateway configured: %d, want 409 (must not silently create an unguarded workspace)", got)
	}
	g.h.GuardProxyURL = "http://igloo-guard.test:3128"
	rec := create(map[string]any{"name": "a", "template_id": "tpl", "guard": map[string]any{"allow": []string{"a.example"}, "routes": []map[string]string{{"name": "llm", "upstream": "https://api.example.com", "secret": "KEY"}}}})
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var ws models.Workspace
	json.Unmarshal(rec.Body.Bytes(), &ws)
	stored, _ := g.h.Store.GetWorkspace(context.Background(), ws.ID)
	pol, guarded := policyOf(stored)
	if !guarded || len(pol.Allow) != 1 || pol.Routes[0].Name != "llm" {
		t.Errorf("stored policy = %+v (guarded=%v)", pol, guarded)
	}
	// An ordinary workspace is still created as before.
	if got := create(map[string]any{"name": "b", "template_id": "tpl"}).Code; got != 201 {
		t.Errorf("unguarded create: %d, want 201", got)
	}
}

// The chart names the server's Service after the release, and lets the operator pick the namespace
// workspaces run in. Neither can be assumed, and a user must not be able to choose the namespace.
func TestClaimVarsCarryTheServerServiceAndWorkspaceNamespaceFromTheDeployment(t *testing.T) {
	g := newGuardRig(t)
	ws := g.workspace(t, "w1", &guard.Policy{Allow: []string{"example.com"}})
	ws.Vars["workspace_namespace"] = "kube-system"

	t.Setenv("IGLOO_SERVER_URL", "http://acme-server.platform.svc.cluster.local")
	t.Setenv("IGLOO_WORKSPACE_NAMESPACE", "acme-ws")
	v := g.h.claimVars(ws)
	if v["guard_server_service"] != "acme-server" {
		t.Errorf("guard_server_service = %q, want acme-server", v["guard_server_service"])
	}
	if v["workspace_namespace"] != "acme-ws" {
		t.Errorf("workspace_namespace = %q: the operator's value must win over the user's", v["workspace_namespace"])
	}

	t.Setenv("IGLOO_WORKSPACE_NAMESPACE", "")
	if got, set := g.h.claimVars(ws)["workspace_namespace"]; set {
		t.Errorf("a user-supplied workspace_namespace survived: %q", got)
	}

	// An address that is not an in-cluster Service name says nothing about the Service.
	t.Setenv("IGLOO_SERVER_URL", "https://igloo.example.com")
	if got, set := g.h.claimVars(ws)["guard_server_service"]; set {
		t.Errorf("guard_server_service = %q for a public URL", got)
	}
}
