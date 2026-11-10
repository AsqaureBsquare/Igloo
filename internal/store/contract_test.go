package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/igloo-dev/igloo/internal/models"
)

// stores returns every Store implementation that can run in this environment.
// Postgres joins when IGLOO_TEST_DATABASE_URL points at a disposable database.
func stores(t *testing.T) map[string]func(t *testing.T) Store {
	out := map[string]func(t *testing.T) Store{
		"memory": func(t *testing.T) Store { return NewMemory() },
	}
	if url := os.Getenv("IGLOO_TEST_DATABASE_URL"); url != "" {
		out["postgres"] = func(t *testing.T) Store {
			s, err := NewPostgresStore(context.Background(), url)
			if err != nil {
				t.Fatalf("postgres: %v", err)
			}
			// Isolate each test: start from empty tables.
			if _, err := s.db.Exec(context.Background(),
				`TRUNCATE users, workspaces, templates, jobs, api_tokens, guard_events, agent_owners RESTART IDENTITY CASCADE`); err != nil {
				t.Fatalf("truncate: %v", err)
			}
			t.Cleanup(s.db.Close)
			return s
		}
	}
	return out
}

func forEachStore(t *testing.T, fn func(t *testing.T, s Store)) {
	for name, mk := range stores(t) {
		mk := mk
		t.Run(name, func(t *testing.T) { fn(t, mk(t)) })
	}
}

func TestUsers(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		u := &models.User{ID: "u1", Email: "a@x.dev", Name: "A", PasswordHash: "h", IsAdmin: true, CreatedAt: time.Now()}
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateUser(ctx, &models.User{ID: "u2", Email: "a@x.dev", CreatedAt: time.Now()}); !errors.Is(err, ErrConflict) {
			t.Errorf("duplicate email: got %v, want ErrConflict", err)
		}
		got, err := s.GetUserByEmail(ctx, "a@x.dev")
		if err != nil || got.ID != "u1" || !got.IsAdmin || got.PasswordHash != "h" {
			t.Errorf("GetUserByEmail = %+v, %v", got, err)
		}
		if _, err := s.GetUser(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("missing user: got %v, want ErrNotFound", err)
		}
		if !got.PasswordChangedAt.IsZero() {
			t.Errorf("a new user has PasswordChangedAt = %v, want zero", got.PasswordChangedAt)
		}
		got.Name = "Renamed"
		if err := s.UpdateUser(ctx, got); err != nil {
			t.Fatal(err)
		}
		if g, _ := s.GetUser(ctx, "u1"); g.Name != "Renamed" {
			t.Errorf("update lost: %+v", g)
		}
		changed := time.Now().UTC().Truncate(time.Second)
		got.PasswordChangedAt = changed
		if err := s.UpdateUser(ctx, got); err != nil {
			t.Fatal(err)
		}
		if g, _ := s.GetUser(ctx, "u1"); !g.PasswordChangedAt.Equal(changed) {
			t.Errorf("PasswordChangedAt = %v, want %v", g.PasswordChangedAt, changed)
		}
		if err := s.DeleteUser(ctx, "u1"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetUser(ctx, "u1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("after delete: got %v, want ErrNotFound", err)
		}
	})
}

func TestWorkspaces(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now()
		mk := func(id, user, org string, st models.WorkspaceStatus) *models.Workspace {
			return &models.Workspace{
				ID: id, UserID: user, OrgID: org, Name: id, Status: st,
				Vars: map[string]string{"k": "v"}, Meta: map[string]string{"m": "n"},
				CreatedAt: now, UpdatedAt: now, LastActiveAt: now,
			}
		}
		for _, w := range []*models.Workspace{
			mk("w1", "alice", "o1", models.StatusRunning),
			mk("w2", "alice", "o2", models.StatusRunning),
			mk("w3", "bob", "o1", models.StatusRunning),
			mk("w4", "alice", "o1", models.StatusDeleted),
		} {
			if err := s.CreateWorkspace(ctx, w); err != nil {
				t.Fatal(err)
			}
		}
		ids := func(user, org string) string {
			ws, err := s.ListWorkspaces(ctx, user, org)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, w := range ws {
				seen[w.ID] = true
			}
			return fmt.Sprint(len(seen), seen["w1"], seen["w2"], seen["w3"], seen["w4"])
		}
		if got, want := ids("alice", ""), fmt.Sprint(2, true, true, false, false); got != want {
			t.Errorf("alice/all orgs: %s, want %s (deleted must be hidden)", got, want)
		}
		if got, want := ids("alice", "o1"), fmt.Sprint(1, true, false, false, false); got != want {
			t.Errorf("alice/o1: %s, want %s", got, want)
		}
		if got, want := ids("", "o1"), fmt.Sprint(2, true, false, true, false); got != want {
			t.Errorf("anyone/o1: %s, want %s", got, want)
		}
		w, err := s.GetWorkspace(ctx, "w1")
		if err != nil || w.Vars["k"] != "v" || w.Meta["m"] != "n" {
			t.Errorf("maps did not round-trip: %+v, %v", w, err)
		}
		if _, err := s.GetWorkspace(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("missing workspace: got %v, want ErrNotFound", err)
		}
	})
}

func TestListOrdering(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		base := time.Now().Add(-time.Hour)
		for i, id := range []string{"w-oldest", "w-middle", "w-newest"} {
			ts := base.Add(time.Duration(i) * time.Minute)
			if err := s.CreateWorkspace(ctx, &models.Workspace{ID: id, UserID: "u", Name: id, Status: models.StatusRunning,
				CreatedAt: ts, UpdatedAt: ts, LastActiveAt: ts}); err != nil {
				t.Fatal(err)
			}
		}
		ws, _ := s.ListWorkspaces(ctx, "u", "")
		if len(ws) != 3 || ws[0].ID != "w-newest" || ws[2].ID != "w-oldest" {
			t.Errorf("workspaces should list newest first, got %v", func() []string {
				var ids []string
				for _, w := range ws {
					ids = append(ids, w.ID)
				}
				return ids
			}())
		}
		for _, n := range []string{"zeta", "alpha", "mid"} {
			s.UpsertTemplate(ctx, &models.Template{ID: n, Name: n, Builtin: true, CreatedAt: base, UpdatedAt: base})
		}
		ts, _ := s.ListTemplates(ctx, "")
		if len(ts) != 3 || ts[0].Name != "alpha" || ts[2].Name != "zeta" {
			t.Error("templates should list alphabetically by name")
		}
	})
}

func TestJobsClaimOrderAndExclusivity(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		if j, err := s.ClaimNextJob(ctx, "p"); j != nil || err != nil {
			t.Fatalf("empty queue: got %+v, %v; want nil, nil", j, err)
		}
		base := time.Now().Add(-time.Hour)
		for i, id := range []string{"j-old", "j-mid", "j-new"} {
			err := s.CreateJob(ctx, &models.ProvisionJob{
				ID: id, WorkspaceID: "w", Action: models.JobStart, Status: models.JobPending,
				CreatedAt: base.Add(time.Duration(i) * time.Minute), UpdatedAt: base,
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		j, err := s.ClaimNextJob(ctx, "daemon-1")
		if err != nil || j == nil || j.ID != "j-old" || j.Status != models.JobRunning || j.ProvisionerID != "daemon-1" {
			t.Fatalf("first claim = %+v, %v; want oldest job running", j, err)
		}

		// Two daemons racing for the last two jobs must never receive the same one.
		var mu sync.Mutex
		claimed := map[string]int{}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if j, err := s.ClaimNextJob(ctx, fmt.Sprintf("d%d", i)); err == nil && j != nil {
					mu.Lock()
					claimed[j.ID]++
					mu.Unlock()
				}
			}(i)
		}
		wg.Wait()
		if len(claimed) != 2 {
			t.Errorf("claimed %v; want exactly the 2 remaining jobs", claimed)
		}
		for id, n := range claimed {
			if n != 1 {
				t.Errorf("job %s claimed %d times", id, n)
			}
		}
	})
}

func TestAPITokens(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		tok := &models.APIToken{ID: "t1", UserID: "u1", TokenHash: "hash1", TokenPrefix: "igl_abc", Name: "ci",
			ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now(), CreatedBy: "u1"}
		if err := s.CreateAPIToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetAPITokenByHash(ctx, "hash1")
		if err != nil || got.ID != "t1" {
			t.Fatalf("by hash = %+v, %v", got, err)
		}
		if _, err := s.GetAPITokenByHash(ctx, "other"); !errors.Is(err, ErrNotFound) {
			t.Errorf("unknown hash: got %v, want ErrNotFound", err)
		}
		if err := s.TouchAPIToken(ctx, "t1"); err != nil {
			t.Fatal(err)
		}
		if g, _ := s.GetAPITokenByHash(ctx, "hash1"); g.LastUsedAt.IsZero() {
			t.Error("TouchAPIToken did not record last use")
		}
		if err := s.DeleteAPIToken(ctx, "t1"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetAPITokenByHash(ctx, "hash1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("revoked token still resolves: %v", err)
		}
	})
}

func TestTemplates(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now()
		tpl := func(id, org string, builtin bool) *models.Template {
			return &models.Template{ID: id, OrgID: org, Name: id, Provider: "kubernetes", Builtin: builtin,
				Version: 3, Files: map[string]string{"main.tf": "x"}, CreatedAt: now, UpdatedAt: now}
		}
		for _, x := range []*models.Template{tpl("b", "", true), tpl("o1", "org1", false), tpl("o2", "org2", false)} {
			if err := s.UpsertTemplate(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		names := func(org string) string {
			ts, err := s.ListTemplates(ctx, org)
			if err != nil {
				t.Fatal(err)
			}
			var out string
			for _, x := range ts {
				out += x.ID + ","
			}
			return out
		}
		if got := names(""); got != "b," {
			t.Errorf("no org: %q, want builtins only", got)
		}
		if got := names("org1"); got != "b,o1," {
			t.Errorf("org1: %q, want builtin + own", got)
		}
		got, err := s.GetTemplate(ctx, "o1")
		if err != nil || got.OrgID != "org1" || got.Version != 3 || got.Files["main.tf"] != "x" {
			t.Errorf("round trip lost fields: %+v, %v", got, err)
		}
		if _, err := s.GetTemplate(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("missing template: got %v, want ErrNotFound", err)
		}
	})
}

func TestGuardEvents(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		g, ok := s.(GuardEventStore)
		if !ok {
			t.Skip("store keeps no guard events")
		}
		ctx := context.Background()
		base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
		var batch []models.GuardEvent
		for i := 0; i < 5; i++ {
			batch = append(batch, models.GuardEvent{Time: base.Add(time.Duration(i) * time.Minute), WorkspaceID: "w1", Kind: "connect",
				Decision: "allow", Host: fmt.Sprintf("h%d.example:443", i), Rule: "*.example", Status: 200, Bytes: int64(i)})
		}
		batch = append(batch, models.GuardEvent{Time: base, WorkspaceID: "w2", Kind: "route", Decision: "deny", Host: "other"})
		if err := g.AppendGuardEvents(ctx, batch); err != nil {
			t.Fatal(err)
		}
		if err := g.AppendGuardEvents(ctx, nil); err != nil {
			t.Fatalf("empty batch: %v", err)
		}

		got, err := g.ListGuardEvents(ctx, "w1", 0)
		if err != nil || len(got) != 5 {
			t.Fatalf("w1 events = %d, %v; want 5 and only w1's", len(got), err)
		}
		if got[0].Host != "h4.example:443" || got[4].Host != "h0.example:443" {
			t.Errorf("order = %s ... %s, want newest first", got[0].Host, got[4].Host)
		}
		if got[0].Rule != "*.example" || got[0].Status != 200 || got[0].Bytes != 4 || !got[0].Time.Equal(base.Add(4*time.Minute)) {
			t.Errorf("fields did not round-trip: %+v", got[0])
		}
		if two, _ := g.ListGuardEvents(ctx, "w1", 2); len(two) != 2 || two[0].Host != "h4.example:443" {
			t.Errorf("limit 2 = %+v", two)
		}
		if none, _ := g.ListGuardEvents(ctx, "nobody", 10); len(none) != 0 {
			t.Errorf("unknown workspace has %d events", len(none))
		}
	})
}

// The agent-owner lease is what lets a second server replica find the agent. The rules:
// the most recent connection wins, a stale replica can neither renew nor delete a row
// it lost, and an unrenewed lease lapses on its own.
func TestAgentOwnerLease(t *testing.T) {
	forEachStore(t, func(t *testing.T, s Store) {
		os, ok := s.(AgentOwnerStore)
		if !ok {
			t.Skipf("%T does not support agent ownership", s)
		}
		ctx := context.Background()

		if o, err := os.GetAgentOwner(ctx, "w1"); err != nil || o != nil {
			t.Fatalf("empty store: owner=%v err=%v, want nil, nil", o, err)
		}

		live := time.Now().Add(5 * time.Second)
		if err := os.UpsertAgentOwner(ctx, &models.AgentOwner{WorkspaceID: "w1", ReplicaID: "a", PodAddr: "10.0.0.1:8081", LeaseUntil: live}); err != nil {
			t.Fatal(err)
		}
		o, err := os.GetAgentOwner(ctx, "w1")
		if err != nil || o == nil || o.ReplicaID != "a" || o.PodAddr != "10.0.0.1:8081" {
			t.Fatalf("after claim: owner=%+v err=%v", o, err)
		}

		// The agent reconnected to b: b's claim overwrites a's unexpired lease.
		if err := os.UpsertAgentOwner(ctx, &models.AgentOwner{WorkspaceID: "w1", ReplicaID: "b", PodAddr: "10.0.0.2:8081", LeaseUntil: live}); err != nil {
			t.Fatal(err)
		}
		// a, with its stale half-dead connection, can neither renew nor delete the row back.
		if err := os.RenewAgentOwner(ctx, "w1", "a", time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := os.DeleteAgentOwner(ctx, "w1", "a"); err != nil {
			t.Fatal(err)
		}
		if o, _ := os.GetAgentOwner(ctx, "w1"); o == nil || o.ReplicaID != "b" {
			t.Fatalf("the stale replica took the workspace back: owner=%+v", o)
		}

		// The owner itself may delete its row (clean disconnect).
		if err := os.DeleteAgentOwner(ctx, "w1", "b"); err != nil {
			t.Fatal(err)
		}
		if o, _ := os.GetAgentOwner(ctx, "w1"); o != nil {
			t.Fatalf("row survived the owner's delete: %+v", o)
		}

		// An expired lease is the same as no owner.
		if err := os.UpsertAgentOwner(ctx, &models.AgentOwner{WorkspaceID: "w1", ReplicaID: "a", PodAddr: "x", LeaseUntil: time.Now().Add(-time.Second)}); err != nil {
			t.Fatal(err)
		}
		if o, _ := os.GetAgentOwner(ctx, "w1"); o != nil {
			t.Fatalf("a lapsed lease still answered: %+v", o)
		}
	})
}
