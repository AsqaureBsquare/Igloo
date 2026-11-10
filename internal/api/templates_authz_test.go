package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/igloo-dev/igloo/internal/models"
)

// A template is Terraform the provisioner executes, and a workspace is only as confined as its
// template makes it. Outside an organization, only a site admin may write one.
func TestOnlyAnAdminMayWriteTemplates(t *testing.T) {
	h := newTestHandler()
	ctx := context.Background()
	now := time.Now()
	h.Store.CreateUser(ctx, &models.User{ID: "root", Email: "root@x.dev", IsAdmin: true, CreatedAt: now})
	h.Store.CreateUser(ctx, &models.User{ID: "mallory", Email: "m@x.dev", CreatedAt: now})
	h.Store.UpsertTemplate(ctx, &models.Template{ID: "tpl", Name: "tpl", CreatedAt: now, UpdatedAt: now})

	r := chi.NewRouter()
	r.Post("/api/templates", h.CreateTemplate)
	r.Put("/api/templates/{id}", h.UpdateTemplate)
	r.Delete("/api/templates/{id}", h.DeleteTemplate)
	call := func(userID, method, path string) int {
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(`{"name":"x","provider":"kubernetes","files":{"main.tf":"anything"}}`)))
		req = req.WithContext(context.WithValue(req.Context(), ctxKeyUserID, userID))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	for _, c := range []struct{ method, path string }{
		{"POST", "/api/templates"}, {"PUT", "/api/templates/tpl"}, {"DELETE", "/api/templates/tpl"},
	} {
		if code := call("mallory", c.method, c.path); code != http.StatusForbidden {
			t.Errorf("%s %s as a plain user: %d, want 403", c.method, c.path, code)
		}
		if code := call("ghost", c.method, c.path); code != http.StatusForbidden {
			t.Errorf("%s %s as an unknown user: %d, want 403", c.method, c.path, code)
		}
	}
	if code := call("root", "POST", "/api/templates"); code != http.StatusCreated {
		t.Errorf("POST as admin: %d, want 201", code)
	}
	if code := call("root", "PUT", "/api/templates/tpl"); code != http.StatusOK {
		t.Errorf("PUT as admin: %d, want 200", code)
	}
}
