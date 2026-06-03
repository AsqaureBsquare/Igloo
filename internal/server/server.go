package server

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/igloo-dev/igloo/internal/api"
	"github.com/igloo-dev/igloo/internal/auth"
	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/relay"
	"github.com/igloo-dev/igloo/internal/store"
)

//go:embed static
var uiFS embed.FS

type Server struct {
	handler *api.Handler
	router  *chi.Mux
}

func New(st store.Store, a *auth.Auth) *Server {
	h := &api.Handler{
		Store: st,
		Auth:  a,
		Relay: relay.New(),
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))
	r.Use(cors)

	// ─── Public routes ────────────────────────────────────────────────────────
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Post("/api/auth/register", h.Register)
	r.Post("/api/auth/login", h.Login)
	r.Get("/ws", h.WebSocket)

	// ─── Authenticated routes ─────────────────────────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(h.RequireAuth)

		r.Get("/api/auth/me", h.Me)
		r.Put("/api/auth/me/ssh-key", h.UpdateSSHKey)

		r.Get("/api/workspaces", h.ListWorkspaces)
		r.Post("/api/workspaces", h.CreateWorkspace)
		r.Get("/api/workspaces/{id}", h.GetWorkspace)
		r.Post("/api/workspaces/{id}/start", h.StartWorkspace)
		r.Post("/api/workspaces/{id}/stop", h.StopWorkspace)
		r.Delete("/api/workspaces/{id}", h.DeleteWorkspace)
		r.Get("/api/workspaces/{id}/terminal", h.TerminalToken)

		r.Get("/api/templates", h.ListTemplates)
		r.Get("/api/templates/{id}", h.GetTemplate)
		r.Get("/api/templates/{id}/files", h.GetTemplateFiles)
		r.Post("/api/templates", h.CreateTemplate)
		r.Put("/api/templates/{id}", h.UpdateTemplate)

		r.Get("/api/workspaces/{id}/logs", h.WorkspaceLogs)
		r.Get("/api/workspaces/{id}/agent-logs", h.AgentLogs)
		r.Get("/api/workspaces/{id}/vscode-token", h.VScodeToken)
		r.Get("/api/workspaces/{id}/forward-token", h.ForwardToken)

		// ─── Admin routes ─────────────────────────────────────────────────────
		r.Group(func(r chi.Router) {
			r.Use(h.RequireAdmin)
			r.Get("/api/admin/users", h.AdminListUsers)
			r.Put("/api/admin/users/{id}", h.AdminUpdateUser)
			r.Delete("/api/admin/users/{id}", h.AdminDeleteUser)
		})
	})

	// SSH / TCP port-forward — token auth via ?token= query param
	r.Get("/api/workspaces/{id}/forward", h.Forward)

	// VS Code proxy — cookie auth handled inside ProxyAuth middleware
	r.Route("/proxy/{id}", func(r chi.Router) {
		r.Use(h.ProxyAuth)
		r.HandleFunc("/", h.Proxy)
		r.HandleFunc("/*", h.Proxy)
	})

	// ─── Embedded UI (catch-all) ──────────────────────────────────────────────
	uiSub, err := fs.Sub(uiFS, "static")
	if err != nil {
		panic(err)
	}
	r.Handle("/*", spaHandler(uiSub))

	return &Server{handler: h, router: r}
}

func (s *Server) SeedBuiltinTemplates(ctx context.Context) {
	templates := []*models.Template{
		{
			ID:          "tpl-docker",
			Name:        "Docker",
			Description: "Run a workspace in a local Docker container. No cloud account needed.",
			Provider:    "docker",
			Icon:        "🐳",
			Builtin:     true,
			Variables:   []models.TemplateVar{},
			Files:       loadTemplateFiles("docker"),
		},
		{
			ID:          "tpl-docker-terraform",
			Name:        "Docker (Terraform)",
			Description: "Same as Docker but provisioned via Terraform. Use this to test the Terraform workflow locally.",
			Provider:    "docker-terraform",
			Icon:        "🏗️",
			Builtin:     true,
			Variables:   []models.TemplateVar{},
			Files:       loadTemplateFiles("docker"),
		},
		{
			ID:          "tpl-aws-fargate",
			Name:        "AWS Fargate",
			Description: "Serverless Fargate task with persistent EBS volume. Sleeps to $0.",
			Provider:    "aws-fargate",
			Icon:        "🟠",
			Builtin:     true,
			Variables: []models.TemplateVar{
				{
					Name:        "cpu",
					Description: "vCPU units",
					Default:     "512",
					Type:        "select",
					Options:     []string{"256", "512", "1024", "2048", "4096"},
				},
				{
					Name:        "memory",
					Description: "Memory (MB)",
					Default:     "1024",
					Type:        "select",
					Options:     []string{"512", "1024", "2048", "4096", "8192"},
				},
				{
					Name:        "region",
					Description: "AWS region",
					Default:     "us-east-1",
					Type:        "select",
					Options:     []string{"us-east-1", "us-west-2", "eu-west-1", "ap-southeast-1"},
				},
			},
			Files: loadTemplateFiles("aws-fargate"),
		},
		{
			ID:          "tpl-fly",
			Name:        "Fly.io",
			Description: "Fly Machine with persistent volume. Deploys globally.",
			Provider:    "fly",
			Icon:        "🪰",
			Builtin:     true,
			Variables: []models.TemplateVar{
				{Name: "region", Description: "Fly.io region", Default: "iad", Type: "string"},
			},
		},
		{
			ID:          "tpl-gcp",
			Name:        "GCP Cloud Run",
			Description: "Serverless Cloud Run job with Filestore persistence.",
			Provider:    "gcp-cloudrun",
			Icon:        "🔵",
			Builtin:     true,
			Variables: []models.TemplateVar{
				{Name: "project_id", Description: "GCP project ID", Required: true, Type: "string"},
				{Name: "region", Description: "GCP region", Default: "us-central1", Type: "string"},
			},
		},
	}

	for _, t := range templates {
		t.CreatedAt = time.Now()
		if err := s.handler.Store.UpsertTemplate(ctx, t); err != nil {
			log.Printf("[server] failed to seed template %s: %v", t.ID, err)
		}
	}
	log.Printf("[server] seeded %d built-in templates", len(templates))
}

// loadTemplateFiles reads .tf files from templates/<name>/ into a map.
// Returns nil (not an error) if the directory doesn't exist.
func loadTemplateFiles(name string) map[string]string {
	srcDir := filepath.Join("templates", name)
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

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// cors adds permissive CORS headers for local development.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// spaHandler serves static files and falls back to index.html for SPA client-side routing.
func spaHandler(static fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(static, path); err != nil {
			// Unknown path — let the SPA router handle it
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
