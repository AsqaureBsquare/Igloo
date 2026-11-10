package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/igloo-dev/igloo/internal/models"
)

func logsRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(h.RequireAuth)
		r.Get("/api/workspaces/{id}/logs-ticket", h.LogsTicket)
	})
	r.Get("/api/workspaces/{id}/provision-logs", h.ProvisionLogs)
	return r
}

func logsFixture(t *testing.T) (*Handler, http.Handler, string, string) {
	h := newTestHandler()
	h.LogBuf = NewLogBuffer()
	now := time.Now()
	for _, w := range []*models.Workspace{
		{ID: "w-alice", UserID: "alice", Status: models.StatusCreating, CreatedAt: now, UpdatedAt: now, LastActiveAt: now},
		{ID: "w-bob", UserID: "bob", Status: models.StatusCreating, CreatedAt: now, UpdatedAt: now, LastActiveAt: now},
	} {
		if err := h.Store.CreateWorkspace(context.Background(), w); err != nil {
			t.Fatal(err)
		}
	}
	alice, _ := h.Auth.SignUser("alice", "a@x.dev")
	bob, _ := h.Auth.SignUser("bob", "b@x.dev")
	return h, logsRouter(h), alice, bob
}

func do(rt http.Handler, path, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	// SSE handlers block until the client goes away; give them an already-cancelled context.
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func ticketFor(t *testing.T, rt http.Handler, ws, bearer string) string {
	rec := do(rt, "/api/workspaces/"+ws+"/logs-ticket", bearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("ticket for %s: %d %s", ws, rec.Code, rec.Body)
	}
	var out struct{ Ticket string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Ticket
}

func TestLogsTicketOnlyForOwner(t *testing.T) {
	_, rt, alice, bob := logsFixture(t)
	if rec := do(rt, "/api/workspaces/w-alice/logs-ticket", bob); rec.Code != http.StatusNotFound {
		t.Errorf("bob asking for alice's ticket: %d, want 404", rec.Code)
	}
	if rec := do(rt, "/api/workspaces/w-alice/logs-ticket", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d, want 401", rec.Code)
	}
	if ticketFor(t, rt, "w-alice", alice) == "" {
		t.Error("owner got an empty ticket")
	}
}

func TestProvisionLogsAuth(t *testing.T) {
	h, rt, alice, bob := logsFixture(t)
	ticket := ticketFor(t, rt, "w-alice", alice)

	if rec := do(rt, "/api/workspaces/w-alice/provision-logs?ticket="+ticket, ""); rec.Code != http.StatusOK {
		t.Errorf("valid ticket: %d, want 200", rec.Code)
	}
	if rec := do(rt, "/api/workspaces/w-bob/provision-logs?ticket="+ticket, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("alice's ticket on bob's workspace: %d, want 401", rec.Code)
	}
	// The long-lived session token must no longer work from the query string.
	if rec := do(rt, "/api/workspaces/w-alice/provision-logs?token="+alice, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("session token in query: %d, want 401", rec.Code)
	}
	// Other connection-token roles are not log tickets.
	for _, role := range []string{"agent", "forward", "proxy", "browser"} {
		tok, _ := h.Auth.SignConn("w-alice", role, time.Minute)
		if rec := do(rt, "/api/workspaces/w-alice/provision-logs?ticket="+tok, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s token as ticket: %d, want 401", role, rec.Code)
		}
	}
	// Header auth still works for the owner only.
	if rec := do(rt, "/api/workspaces/w-alice/provision-logs", alice); rec.Code != http.StatusOK {
		t.Errorf("owner with header: %d, want 200", rec.Code)
	}
	if rec := do(rt, "/api/workspaces/w-alice/provision-logs", bob); rec.Code != http.StatusNotFound {
		t.Errorf("other user with header: %d, want 404", rec.Code)
	}
	// Unauthenticated callers can't tell real workspaces from fake ones.
	a := do(rt, "/api/workspaces/w-alice/provision-logs", "").Code
	b := do(rt, "/api/workspaces/does-not-exist/provision-logs", "").Code
	if a != http.StatusUnauthorized || b != http.StatusUnauthorized {
		t.Errorf("anonymous probe: existing=%d missing=%d, want both 401", a, b)
	}
}

func TestLogsTicketExpires(t *testing.T) {
	h, rt, _, _ := logsFixture(t)
	expired, _ := h.Auth.SignConn("w-alice", logsTicketRole, -time.Second)
	if rec := do(rt, "/api/workspaces/w-alice/provision-logs?ticket="+expired, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("expired ticket: %d, want 401", rec.Code)
	}
}
