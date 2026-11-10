package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/igloo-dev/igloo/internal/relay"
)

func TestBearerOrQuery(t *testing.T) {
	cases := []struct {
		name, auth, query, want string
	}{
		{"header only", "Bearer h-tok", "", "h-tok"},
		{"query only", "", "token=q-tok", "q-tok"},
		{"header wins over query", "Bearer h-tok", "token=q-tok", "h-tok"},
		{"nothing", "", "", ""},
		// A bad header must not be rescued by a valid query token.
		{"empty bearer header does not fall back", "Bearer ", "token=q-tok", ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/x?"+c.query, nil)
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		if got := connToken(req); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The three connection endpoints authenticate before upgrading. For a plain GET,
// "authenticated" shows up as anything other than 401: 400 (upgrade refused), 403
// (wrong role) or 503 (no agent).
func TestConnectionEndpointsAcceptHeaderAndQueryTokens(t *testing.T) {
	h := newTestHandler()
	h.Relay = relay.New()
	r := chi.NewRouter()
	r.Get("/ws", h.WebSocket)
	r.Get("/api/workspaces/{id}/forward", h.Forward)
	r.Get("/api/workspaces/{id}/tailnet", h.TailnetWS)

	agentTok, _ := h.Auth.SignConn("w1", "agent", time.Hour)
	fwdTok, _ := h.Auth.SignConn("w1", "forward", time.Hour)
	otherWS, _ := h.Auth.SignConn("w2", "forward", time.Hour)

	do := func(path, bearer string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	type tc struct {
		name, path, bearer string
		want               int
	}
	for _, c := range []tc{
		// /ws (agent and browser connections)
		{"ws: header token", "/ws", agentTok, http.StatusBadRequest},
		{"ws: legacy query token", "/ws?token=" + agentTok, "", http.StatusBadRequest},
		{"ws: no token", "/ws", "", http.StatusUnauthorized},
		{"ws: bad header, good query", "/ws?token=" + agentTok, "garbage", http.StatusUnauthorized},
		// port forward
		{"forward: header token", "/api/workspaces/w1/forward", fwdTok, http.StatusServiceUnavailable},
		{"forward: legacy query token", "/api/workspaces/w1/forward?token=" + fwdTok, "", http.StatusServiceUnavailable},
		{"forward: other workspace's token", "/api/workspaces/w1/forward", otherWS, http.StatusUnauthorized},
		{"forward: agent token is not a forward token", "/api/workspaces/w1/forward", agentTok, http.StatusUnauthorized},
		{"forward: no token", "/api/workspaces/w1/forward", "", http.StatusUnauthorized},
		// tailnet coordinator: agent token used with role=client is rejected after auth passes
		{"tailnet: header token", "/api/workspaces/w1/tailnet?role=client", agentTok, http.StatusForbidden},
		{"tailnet: legacy query token", "/api/workspaces/w1/tailnet?role=client&token=" + agentTok, "", http.StatusForbidden},
		{"tailnet: no token", "/api/workspaces/w1/tailnet?role=client", "", http.StatusUnauthorized},
		{"tailnet: other workspace's token", "/api/workspaces/w1/tailnet?role=client", otherWS, http.StatusUnauthorized},
	} {
		if got := do(c.path, c.bearer); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
