package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/igloo-dev/igloo/internal/guard"
	"github.com/igloo-dev/igloo/internal/models"
)

// baselineRig is the guard rig plus the baseline and sandbox routes, with alice an administrator.
func baselineRig(t *testing.T) *guardRig {
	g := newGuardRig(t)
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(g.h.GuardAuth)
		r.Post("/api/guard/authorize", g.h.GuardAuthorize)
	})
	r.Group(func(r chi.Router) {
		r.Use(g.h.RequireAuth)
		r.Post("/api/workspaces", g.h.CreateWorkspace)
		r.Post("/api/sandboxes", g.h.CreateSandbox)
		r.Post("/api/workspaces/{id}/start", g.h.StartWorkspace)
		r.Get("/api/guard-baseline", g.h.GetGuardBaseline)
		r.Get("/api/workspaces/{id}/guard-policy", g.h.GetGuardPolicy)
		r.Put("/api/workspaces/{id}/guard-policy", g.h.PutGuardPolicy)
		r.Group(func(r chi.Router) {
			r.Use(g.h.RequireAdmin)
			r.Put("/api/admin/guard-baseline", g.h.PutGuardBaseline)
			r.Put("/api/admin/platform-config", g.h.SavePlatformConfig)
		})
	})
	g.rt = r
	ctx := context.Background()
	g.h.Store.CreateUser(ctx, &models.User{ID: "alice", Email: "a@x.dev", IsAdmin: true})
	g.h.Store.CreateUser(ctx, &models.User{ID: "bob", Email: "b@x.dev"})
	return g
}

func (g *guardRig) setBaseline(t *testing.T, b guard.Baseline) {
	t.Helper()
	if rec := g.do("PUT", "/api/admin/guard-baseline", g.alice, b); rec.Code != 200 {
		t.Fatalf("set baseline: %d %s", rec.Code, rec.Body)
	}
}

func TestOnlyAnAdminSetsTheBaselineButEveryoneCanReadIt(t *testing.T) {
	g := baselineRig(t)
	if got := g.do("PUT", "/api/admin/guard-baseline", g.bob, guard.Baseline{Deny: []string{"a.com"}}).Code; got != 403 {
		t.Errorf("non-admin PUT: %d, want 403", got)
	}
	if got := g.do("PUT", "/api/admin/guard-baseline", g.alice, guard.Baseline{Deny: []string{"*"}}).Code; got != 400 {
		t.Errorf("invalid baseline: %d, want 400", got)
	}
	g.setBaseline(t, guard.Baseline{Deny: []string{"evil.com"}, RequireGuard: true})
	rec := g.do("GET", "/api/guard-baseline", g.bob, nil)
	var b guard.Baseline
	json.Unmarshal(rec.Body.Bytes(), &b)
	if rec.Code != 200 || len(b.Deny) != 1 || !b.RequireGuard {
		t.Errorf("GET as a normal user: %d %+v", rec.Code, b)
	}
	var view struct {
		GatewayEnabled bool `json:"gateway_enabled"`
	}
	json.Unmarshal(rec.Body.Bytes(), &view)
	if !view.GatewayEnabled {
		t.Error("the baseline view should say the gateway is available, so the UI can offer it")
	}
	g.h.GuardProxyURL = ""
	json.Unmarshal(g.do("GET", "/api/guard-baseline", g.bob, nil).Body.Bytes(), &view)
	if view.GatewayEnabled {
		t.Error("gateway_enabled should be false when the server has no gateway configured")
	}
	g.h.GuardProxyURL = "http://igloo-guard.test:3128"
	if got := g.do("GET", "/api/guard-baseline", "", nil).Code; got != 401 {
		t.Errorf("unauthenticated GET: %d, want 401", got)
	}
}

func TestBaselineIsKeptWhenOtherPlatformSettingsAreSaved(t *testing.T) {
	g := baselineRig(t)
	g.setBaseline(t, guard.Baseline{Deny: []string{"evil.com"}})
	if rec := g.do("PUT", "/api/admin/platform-config", g.alice, map[string]string{"tailscale_tailnet": "x.ts.net"}); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	b, _ := g.h.baseline(context.Background())
	if len(b.Deny) != 1 {
		t.Errorf("baseline lost after saving the platform config: %+v", b)
	}
}

func TestCreatingAWorkspaceIsCheckedAgainstTheBaseline(t *testing.T) {
	g := baselineRig(t)
	g.setBaseline(t, guard.Baseline{Deny: []string{"evil.com"}, Ceiling: []string{"*.corp.example"}, ForbidPrivate: true})
	create := func(pol map[string]any) int {
		body := map[string]any{"name": "w", "template_id": "tpl"}
		if pol != nil {
			body["guard"] = pol
		}
		return g.do("POST", "/api/workspaces", g.alice, body).Code
	}
	if got := create(map[string]any{"allow": []string{"git.corp.example"}}); got != 201 {
		t.Errorf("allowed policy: %d, want 201", got)
	}
	for name, pol := range map[string]map[string]any{
		"outside ceiling": {"allow": []string{"example.org"}},
		"denied":          {"allow": []string{"evil.com"}},
		"private":         {"allow": []string{"git.corp.example"}, "allow_private": true},
		"route":           {"routes": []map[string]string{{"name": "r", "upstream": "https://example.org", "secret": "K"}}},
	} {
		if got := create(pol); got != 403 {
			t.Errorf("%s: %d, want 403", name, got)
		}
	}
	if got := create(nil); got != 201 {
		t.Errorf("an unguarded workspace is fine while the baseline does not require a guard: %d", got)
	}
	g.setBaseline(t, guard.Baseline{RequireGuard: true})
	if got := create(nil); got != 403 {
		t.Errorf("unguarded workspace under require_guard: %d, want 403", got)
	}
}

func TestEditingAPolicyIsCheckedAgainstTheBaseline(t *testing.T) {
	g := baselineRig(t)
	g.workspace(t, "w1", &guard.Policy{Allow: []string{"a.example"}})
	g.setBaseline(t, guard.Baseline{Deny: []string{"evil.com"}})
	if got := g.do("PUT", "/api/workspaces/w1/guard-policy", g.alice, guard.Policy{Allow: []string{"evil.com"}}).Code; got != 403 {
		t.Errorf("loosening past the baseline: %d, want 403", got)
	}
	if got := g.do("PUT", "/api/workspaces/w1/guard-policy", g.alice, guard.Policy{Allow: []string{"b.example"}}).Code; got != 200 {
		t.Errorf("a permitted edit: %d, want 200", got)
	}
}

// Tightening the baseline must reach workspaces that already exist, with no one editing them.
func TestTheGatewayIsToldTheTightenedPolicyForExistingWorkspaces(t *testing.T) {
	g := baselineRig(t)
	ws := g.workspace(t, "w1", &guard.Policy{Allow: []string{"ok.example", "evil.com"}, AllowPrivate: true})
	authorize := func() guard.Identity {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/guard/authorize", jsonBody(map[string]string{"token": g.egressToken(ws.ID)}))
		req.Header.Set("Authorization", "Bearer "+testGuardKey)
		g.rt.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("authorize: %d %s", rec.Code, rec.Body)
		}
		var id guard.Identity
		json.Unmarshal(rec.Body.Bytes(), &id)
		return id
	}
	if id := authorize(); len(id.Policy.Allow) != 2 || !id.Policy.AllowPrivate {
		t.Fatalf("before the baseline: %+v", id.Policy)
	}
	g.setBaseline(t, guard.Baseline{Deny: []string{"evil.com"}, ForbidPrivate: true})
	id := authorize()
	if _, ok := id.Policy.Allows("evil.com"); ok || id.Policy.AllowPrivate {
		t.Errorf("after the baseline: %+v", id.Policy)
	}
	if _, ok := id.Policy.Allows("ok.example"); !ok {
		t.Error("an unaffected host was lost")
	}
	// The owner sees both what they set and what is enforced.
	rec := g.do("GET", "/api/workspaces/w1/guard-policy", g.alice, nil)
	var got struct {
		Policy    guard.Policy
		Effective *guard.Policy
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Policy.Allow) != 2 || got.Effective == nil || len(got.Effective.Allow) != 1 {
		t.Errorf("policy view: %s", rec.Body)
	}
}

func TestSandboxesObeyTheBaselineLifetimeAndGuardRequirement(t *testing.T) {
	g := baselineRig(t)
	g.h.SandboxImage = "img"
	g.setBaseline(t, guard.Baseline{MaxSandboxTTLSeconds: 600, RequireGuard: true})
	mk := func(body map[string]any) int {
		body["template_id"] = "tpl"
		return g.do("POST", "/api/sandboxes", g.alice, body).Code
	}
	if got := mk(map[string]any{"ttl_seconds": 600}); got != 201 {
		t.Errorf("ttl at the cap: %d, want 201", got)
	}
	if got := mk(map[string]any{"ttl_seconds": 601}); got != 403 {
		t.Errorf("ttl over the cap: %d, want 403", got)
	}
	if got := mk(map[string]any{}); got != 403 { // default 1h is over the cap
		t.Errorf("default ttl over the cap: %d, want 403", got)
	}
	if got := mk(map[string]any{"ttl_seconds": 60, "unguarded": true}); got != 403 {
		t.Errorf("unguarded under require_guard: %d, want 403", got)
	}
}

func TestSandboxesCreatedBeforeATighterCapExpireByTheCap(t *testing.T) {
	g := baselineRig(t)
	ctx := context.Background()
	now := time.Now()
	long := now.Add(20 * time.Hour).UTC().Format(time.RFC3339)
	mk := func(id string, age time.Duration) {
		g.h.Store.CreateWorkspace(ctx, &models.Workspace{ID: id, UserID: "alice", Provider: "kubernetes", Status: models.StatusRunning,
			Meta: map[string]string{metaSandbox: "true", metaSandboxExpires: long}, CreatedAt: now.Add(-age), UpdatedAt: now, LastActiveAt: now})
	}
	mk("old", 2*time.Hour)
	mk("young", time.Minute)
	if n := g.h.expireSandboxes(ctx); n != 0 {
		t.Fatalf("no cap yet, %d deleted", n)
	}
	g.setBaseline(t, guard.Baseline{MaxSandboxTTLSeconds: 3600})
	if n := g.h.expireSandboxes(ctx); n != 1 {
		t.Errorf("deleted %d, want only the sandbox older than the cap", n)
	}
	if ws, _ := g.h.Store.GetWorkspace(ctx, "old"); ws.Status != models.StatusDeleting {
		t.Errorf("old sandbox is %s", ws.Status)
	}
	if ws, _ := g.h.Store.GetWorkspace(ctx, "young"); ws.Status != models.StatusRunning {
		t.Errorf("young sandbox is %s", ws.Status)
	}
}

func jsonBody(v any) *bytes.Buffer {
	var b bytes.Buffer
	json.NewEncoder(&b).Encode(v)
	return &b
}

func TestOpenModeReachesTheGatewayAndUsersCannotSelfOpen(t *testing.T) {
	g := baselineRig(t)
	ws := g.workspace(t, "w1", &guard.Policy{Allow: []string{"only.example"}})
	authorize := func() guard.Policy {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/guard/authorize", jsonBody(map[string]string{"token": g.egressToken(ws.ID)}))
		req.Header.Set("Authorization", "Bearer "+testGuardKey)
		g.rt.ServeHTTP(rec, req)
		var id guard.Identity
		json.Unmarshal(rec.Body.Bytes(), &id)
		return id.Policy
	}
	if _, ok := authorize().Allows("other.example"); ok {
		t.Fatal("restricted by default: an unlisted host was allowed")
	}
	if got := g.do("PUT", "/api/admin/guard-baseline", g.alice, map[string]any{"mode": "open", "ceiling": []string{"a.com"}}).Code; got != 400 {
		t.Errorf("open mode with a ceiling: %d, want 400", got)
	}
	g.setBaseline(t, guard.Baseline{Mode: guard.ModeOpen, Deny: []string{"pump.co"}})
	pol := authorize()
	if _, ok := pol.Allows("other.example"); !ok {
		t.Error("open mode: an unlisted host should be reachable")
	}
	if _, ok := pol.Allows("pump.co"); ok {
		t.Error("open mode: a denied host was reachable")
	}
	// A user trying to turn it on for themselves is refused, and a stored allow_all is ignored.
	if got := g.do("PUT", "/api/workspaces/w1/guard-policy", g.alice, map[string]any{"allow_all": true}).Code; got != 400 {
		t.Errorf("a user setting allow_all: %d, want 400", got)
	}
	g.setBaseline(t, guard.Baseline{})
	stored, _ := g.h.Store.GetWorkspace(context.Background(), "w1")
	stored.Meta[metaGuardPolicy] = `{"allow":["only.example"],"allow_all":true}`
	g.h.Store.UpdateWorkspace(context.Background(), stored)
	if _, ok := authorize().Allows("other.example"); ok {
		t.Error("a stored allow_all opened a workspace without an open baseline")
	}
}

func TestAnUnguardedWorkspaceCannotBeStartedWhenTheBaselineRequiresTheGateway(t *testing.T) {
	g := baselineRig(t)
	ctx := context.Background()
	asleep := func(id string, pol *guard.Policy) {
		ws := g.workspace(t, id, pol)
		ws.Status = models.StatusSleeping
		g.h.Store.UpdateWorkspace(ctx, ws)
	}
	asleep("before", nil) // started while the baseline asks for nothing
	asleep("plain", nil)
	asleep("safe", &guard.Policy{})
	start := func(id string) int { return g.do("POST", "/api/workspaces/"+id+"/start", g.alice, nil).Code }

	if got := start("before"); got != 200 {
		t.Errorf("without the requirement an unguarded workspace starts: %d, want 200", got)
	}
	g.setBaseline(t, guard.Baseline{RequireGuard: true})
	if got := start("plain"); got != 403 {
		t.Errorf("unguarded under require_guard: %d, want 403", got)
	}
	if ws, _ := g.h.Store.GetWorkspace(ctx, "plain"); ws.Status != models.StatusSleeping {
		t.Errorf("the refused workspace is now %s, want it left asleep", ws.Status)
	}
	if got := start("safe"); got != 200 {
		t.Errorf("a guarded workspace must still start: %d", got)
	}
}
