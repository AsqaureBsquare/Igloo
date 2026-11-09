package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/igloo-dev/igloo/internal/models"
)

// A workspace connection token (agent/forward/proxy/logs) is not a user session.
// The agent token lives inside every workspace, so if RequireAuth accepted it as a
// user with an empty ID, ListWorkspaces("") would return every user's workspaces.
func TestConnTokenIsNotAUserSession(t *testing.T) {
	h := newTestHandler()
	ctx := context.Background()
	now := time.Now()
	for _, w := range []*models.Workspace{
		{ID: "w-alice", UserID: "alice", Name: "alice-ws", Status: models.StatusRunning, CreatedAt: now, UpdatedAt: now, LastActiveAt: now},
		{ID: "w-bob", UserID: "bob", Name: "bob-ws", Status: models.StatusRunning, CreatedAt: now, UpdatedAt: now, LastActiveAt: now},
	} {
		if err := h.Store.CreateWorkspace(ctx, w); err != nil {
			t.Fatal(err)
		}
	}

	handler := h.RequireAuth(http.HandlerFunc(h.ListWorkspaces))
	for _, role := range []string{"agent", "forward", "proxy", "browser", "logs"} {
		tok, err := h.Auth.SignConn("w-alice", role, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/workspaces", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("role %q: got %d, want 401; body: %s", role, rec.Code, rec.Body)
		}
	}
}
