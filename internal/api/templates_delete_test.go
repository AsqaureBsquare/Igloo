package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/igloo-dev/igloo/internal/models"
)

func deleteTemplate(h *Handler, id string) *httptest.ResponseRecorder {
	return deleteTemplateAs(h, "u-admin", id)
}

func deleteTemplateAs(h *Handler, userID, id string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Delete("/api/templates/{id}", h.DeleteTemplate)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/templates/"+id, nil)
	r.ServeHTTP(rec, req.WithContext(context.WithValue(req.Context(), ctxKeyUserID, userID)))
	return rec
}

func TestDeleteTemplateRefusedWhileWorkspacesUseIt(t *testing.T) {
	h := newTestHandler()
	ctx := context.Background()
	now := time.Now()
	h.Store.CreateUser(ctx, &models.User{ID: "u-admin", Email: "root@x.dev", IsAdmin: true, CreatedAt: now})
	for _, tpl := range []*models.Template{
		{ID: "used", Name: "used", CreatedAt: now},
		{ID: "free", Name: "free", CreatedAt: now},
		{ID: "core", Name: "core", Builtin: true, CreatedAt: now},
	} {
		h.Store.UpsertTemplate(ctx, tpl)
	}
	h.Store.CreateWorkspace(ctx, &models.Workspace{ID: "w1", UserID: "u", TemplateID: "used", Status: models.StatusRunning, CreatedAt: now, UpdatedAt: now, LastActiveAt: now})
	h.Store.CreateWorkspace(ctx, &models.Workspace{ID: "w2", UserID: "u", TemplateID: "free", Status: models.StatusDeleted, CreatedAt: now, UpdatedAt: now, LastActiveAt: now})

	if rec := deleteTemplate(h, "used"); rec.Code != http.StatusConflict {
		t.Errorf("template with a live workspace: %d, want 409", rec.Code)
	}
	if _, err := h.Store.GetTemplate(ctx, "used"); err != nil {
		t.Error("refused delete still removed the template")
	}
	// A workspace that is already deleted no longer pins its template.
	if rec := deleteTemplate(h, "free"); rec.Code != http.StatusNoContent {
		t.Errorf("template with only deleted workspaces: %d, want 204", rec.Code)
	}
	// Deletion is admin-only, so an admin may also delete a builtin template (re-seeding
	// restores the shipped ones at the next start). It still must not be in use.
	if rec := deleteTemplate(h, "core"); rec.Code != http.StatusNoContent {
		t.Errorf("builtin, unused, deleted by an admin: %d, want 204", rec.Code)
	}
	if rec := deleteTemplate(h, "missing"); rec.Code != http.StatusNotFound {
		t.Errorf("missing: %d, want 404", rec.Code)
	}
	// Once the last workspace is gone the template can be deleted.
	h.Store.DeleteWorkspace(ctx, "w1")
	if rec := deleteTemplate(h, "used"); rec.Code != http.StatusNoContent {
		t.Errorf("after workspace removed: %d, want 204", rec.Code)
	}
}
