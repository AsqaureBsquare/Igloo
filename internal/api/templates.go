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

// GET /api/templates
func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	templates, err := h.Store.ListTemplates(r.Context())
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
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

	t := &models.Template{
		ID:          uuid.NewString(),
		Name:        body.Name,
		Description: body.Description,
		Provider:    body.Provider,
		Icon:        body.Icon,
		Variables:   body.Variables,
		Files:       body.Files,
		CreatedAt:   time.Now(),
	}

	if err := h.Store.UpsertTemplate(r.Context(), t); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.json(w, http.StatusCreated, t)
}

// PUT /api/templates/{id} — update template metadata + files (admin only)
func (h *Handler) UpdateTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := h.Store.GetTemplate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.err(w, http.StatusNotFound, "template not found")
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

	if err := h.Store.UpsertTemplate(r.Context(), t); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.json(w, http.StatusOK, t)
}
