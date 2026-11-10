package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/igloo-dev/igloo/internal/models"
)

// Auth is checked before the WebSocket upgrade. A plain GET that passes auth then
// fails the upgrade with 400, which tells "authenticated" apart from 401.
func provisionerWS(h *Handler, path, bearer string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	rec := httptest.NewRecorder()
	h.ProvisionerWS(rec, req)
	return rec.Code
}

func TestProvisionerWSAcceptsKeyInHeader(t *testing.T) {
	h := newTestHandler()
	const key = "prov-secret"
	if err := h.Store.CreateProvisionerKey(context.Background(), &models.ProvisionerKey{
		ID: "k1", Name: "test", TokenHash: hashToken(key), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, path, auth string
		want             int
	}{
		{"header key", "/api/provisioner/ws?id=d1", "Bearer " + key, http.StatusBadRequest}, // authenticated, upgrade fails
		{"legacy query key still works", "/api/provisioner/ws?id=d1&token=" + key, "", http.StatusBadRequest},
		{"no credentials", "/api/provisioner/ws?id=d1", "", http.StatusUnauthorized},
		{"wrong header key", "/api/provisioner/ws?id=d1", "Bearer nope", http.StatusUnauthorized},
		// An invalid header must not be rescued by a valid query token.
		{"bad header, good query", "/api/provisioner/ws?id=d1&token=" + key, "Bearer nope", http.StatusUnauthorized},
		{"header key but no daemon id", "/api/provisioner/ws", "Bearer " + key, http.StatusBadRequest},
	}
	for _, c := range cases {
		if got := provisionerWS(h, c.path, c.auth); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
