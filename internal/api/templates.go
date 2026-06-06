package api

import (
	"net/http"
	"path/filepath"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/igloo-dev/igloo/internal/models"
)

func loadFilesFromDisk(name string) map[string]string {
	srcDir, _ := filepath.Abs(filepath.Join("templates", name))
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return nil
	}
	files := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) == "" {
			continue
		}
		if data, err := os.ReadFile(filepath.Join(srcDir, e.Name())); err == nil {
			files[e.Name()] = string(data)
		}
	}
	return files
}

// GET /api/templates?q=kubernetes
func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	templates, err := h.Store.ListTemplates(r.Context(), ctxOrgID(r))
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if q := r.URL.Query().Get("q"); q != "" {
		templates = applyTemplateFilter(templates, ParseTemplateFilter(q))
	}
	if templates == nil {
		templates = []*models.Template{}
	}
	h.json(w, http.StatusOK, templates)
}

// GET /api/templates/{id}
func (h *Handler) GetTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := h.Store.GetTemplate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.err(w, http.StatusNotFound, "template not found")
		return
	}
	h.json(w, http.StatusOK, t)
}

// GET /api/templates/{id}/files — returns the .tf source files
func (h *Handler) GetTemplateFiles(w http.ResponseWriter, r *http.Request) {
	t, err := h.Store.GetTemplate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.err(w, http.StatusNotFound, "template not found")
		return
	}

	// Prefer files stored in DynamoDB; fall back to disk
	files := t.Files
	if len(files) == 0 {
		files = loadFilesFromDisk(t.Provider)
	}
	if files == nil {
		files = map[string]string{}
	}
	h.json(w, http.StatusOK, files)
}

// POST /api/templates
func (h *Handler) CreateTemplate(w http.ResponseWriter, r *http.Request) {
	if o := ctxOrg(r); o != nil && !requireOrgAdmin(h, w, r) {
		return
	}

	var body struct {
		Name        string               `json:"name"`
		Description string               `json:"description"`
		Provider    string               `json:"provider"`
		Icon        string               `json:"icon"`
		Variables   []models.TemplateVar `json:"variables"`
		Files       map[string]string    `json:"files"`
	}
	if err := h.decode(r, &body); err != nil || body.Name == "" || body.Provider == "" {
		h.err(w, http.StatusBadRequest, "name and provider are required")
		return
	}

	now := time.Now()
	t := &models.Template{
		ID:          uuid.NewString(),
		OrgID:       ctxOrgID(r),
		Name:        body.Name,
		Description: body.Description,
		Provider:    body.Provider,
		Icon:        body.Icon,
		Variables:   body.Variables,
		Files:       body.Files,
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := h.Store.UpsertTemplate(r.Context(), t); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.json(w, http.StatusCreated, t)
}

// PUT /api/templates/{id} — update template metadata + files
func (h *Handler) UpdateTemplate(w http.ResponseWriter, r *http.Request) {
	if o := ctxOrg(r); o != nil && !requireOrgAdmin(h, w, r) {
		return
	}

	t, err := h.Store.GetTemplate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.err(w, http.StatusNotFound, "template not found")
		return
	}
	if orgID := ctxOrgID(r); orgID != "" && !t.Builtin && t.OrgID != orgID {
		h.err(w, http.StatusForbidden, "template does not belong to this organization")
		return
	}

	var body struct {
		Name        string               `json:"name"`
		Description string               `json:"description"`
		Icon        string               `json:"icon"`
		Variables   []models.TemplateVar `json:"variables"`
		Files       map[string]string    `json:"files"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid body")
		return
	}

	if body.Name != "" {
		t.Name = body.Name
	}
	if body.Description != "" {
		t.Description = body.Description
	}
	if body.Icon != "" {
		t.Icon = body.Icon
	}
	if body.Variables != nil {
		t.Variables = body.Variables
	}
	if body.Files != nil {
		t.Files = body.Files
	}
	t.Version++
	t.UpdatedAt = time.Now()

	if err := h.Store.UpsertTemplate(r.Context(), t); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.json(w, http.StatusOK, t)
}

// DELETE /api/templates/{id}
func (h *Handler) DeleteTemplate(w http.ResponseWriter, r *http.Request) {
	if o := ctxOrg(r); o != nil && !requireOrgAdmin(h, w, r) {
		return
	}

	id := chi.URLParam(r, "id")
	t, err := h.Store.GetTemplate(r.Context(), id)
	if err != nil {
		h.err(w, http.StatusNotFound, "template not found")
		return
	}
	if t.Builtin {
		h.err(w, http.StatusForbidden, "cannot delete built-in template")
		return
	}
	if orgID := ctxOrgID(r); orgID != "" && t.OrgID != orgID {
		h.err(w, http.StatusForbidden, "template does not belong to this organization")
		return
	}
	if err := h.Store.DeleteTemplate(r.Context(), id); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
