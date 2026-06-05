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
	"github.com/igloo-dev/igloo/internal/tailnet"
)

//go:embed static
var uiFS embed.FS

type Server struct {
	handler *api.Handler
	router  *chi.Mux
}

func New(st store.Store, a *auth.Auth) *Server {
	h := &api.Handler{
		Store:       st,
		Auth:        a,
		Relay:       relay.New(),
		Registry:    api.NewProvisionerRegistry(),
		LogBuf:      api.NewLogBuffer(),
		Coordinator: tailnet.NewCoordinator(),
	}

	// Wire up OIDC if configured via environment variables.
	if issuer := os.Getenv("IGLOO_OIDC_ISSUER_URL"); issuer != "" {
		scopes := strings.Fields(os.Getenv("IGLOO_OIDC_SCOPES"))
		cfg := auth.OIDCConfig{
			IssuerURL:    issuer,
			ClientID:     os.Getenv("IGLOO_OIDC_CLIENT_ID"),
			ClientSecret: os.Getenv("IGLOO_OIDC_CLIENT_SECRET"),
			RedirectURL:  os.Getenv("IGLOO_OIDC_REDIRECT_URL"),
			Scopes:       scopes,
			ButtonText:   os.Getenv("IGLOO_OIDC_BUTTON_TEXT"),
			IconURL:      os.Getenv("IGLOO_OIDC_ICON_URL"),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if p, err := auth.NewOIDCProvider(ctx, cfg); err != nil {
			log.Printf("[oidc] failed to initialise provider: %v", err)
		} else {
			h.OIDC = p
			log.Printf("[oidc] provider ready issuer=%s", issuer)
		}
	}
	h.DisablePasswordAuth = os.Getenv("IGLOO_DISABLE_PASSWORD_AUTH") == "true"

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))
	r.Use(cors)

	// ─── Public routes ────────────────────────────────────────────────────────
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/api/auth/config", h.AuthConfig)
	r.Post("/api/auth/register", h.Register)
	r.Post("/api/auth/login", h.Login)
	r.Get("/api/auth/oidc/login", h.OIDCLogin)
	r.Get("/api/auth/oidc/callback", h.OIDCCallback)
	r.Get("/ws", h.WebSocket)

	// ─── Authenticated routes ─────────────────────────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(h.RequireAuth)

		r.Get("/api/auth/me", h.Me)
		r.Put("/api/auth/me/ssh-key", h.UpdateSSHKey)
		r.Put("/api/auth/me/tailscale-key", h.UpdateTailscaleKey)

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

		// ─── Organizations & Teams ────────────────────────────────────────────
		r.Get("/api/orgs", h.ListOrgs)
		r.Post("/api/orgs", h.CreateOrg)
		r.Get("/api/orgs/{id}", h.GetOrg)
		r.Put("/api/orgs/{id}", h.UpdateOrg)
		r.Delete("/api/orgs/{id}", h.DeleteOrg)
		r.Get("/api/orgs/{id}/members", h.ListOrgMembers)
		r.Post("/api/orgs/{id}/members", h.AddOrgMember)
		r.Put("/api/orgs/{id}/members/{userID}", h.UpdateOrgMember)
		r.Delete("/api/orgs/{id}/members/{userID}", h.RemoveOrgMember)
		r.Get("/api/orgs/{id}/teams", h.ListTeams)
		r.Post("/api/orgs/{id}/teams", h.CreateTeam)
		r.Get("/api/orgs/{id}/teams/{teamID}", h.GetTeam)
		r.Delete("/api/orgs/{id}/teams/{teamID}", h.DeleteTeam)
		r.Get("/api/orgs/{id}/teams/{teamID}/members", h.ListTeamMembers)
		r.Post("/api/orgs/{id}/teams/{teamID}/members", h.AddTeamMember)
		r.Delete("/api/orgs/{id}/teams/{teamID}/members/{userID}", h.RemoveTeamMember)

		// ─── Admin routes ─────────────────────────────────────────────────────
		r.Group(func(r chi.Router) {
			r.Use(h.RequireAdmin)
			r.Get("/api/admin/users", h.AdminListUsers)
			r.Put("/api/admin/users/{id}", h.AdminUpdateUser)
			r.Delete("/api/admin/users/{id}", h.AdminDeleteUser)
			r.Post("/api/admin/provisioner-keys", h.CreateProvisionerKey)
			r.Get("/api/admin/provisioner-keys", h.ListProvisionerKeys)
			r.Delete("/api/admin/provisioner-keys/{id}", h.DeleteProvisionerKey)
			r.Get("/api/admin/platform-config", h.GetPlatformConfig)
			r.Put("/api/admin/platform-config", h.SavePlatformConfig)
		})
	})

	// ─── Provisioner daemon API (key-authenticated) ───────────────────────────
	// WS: token via ?token= query param; HTTP: Bearer token header
	r.Get("/api/provisioner/ws", h.ProvisionerWS)
	r.Group(func(r chi.Router) {
		r.Use(h.ProvisionerAuth)
		r.Post("/api/provisioner/claim", h.ClaimJob)
		r.Post("/api/provisioner/jobs/{id}/ping", h.JobPing)
		r.Post("/api/provisioner/jobs/{id}/done", h.JobDone)
		r.Post("/api/provisioner/jobs/{id}/fail", h.JobFail)
	})

	// SSE log stream — token via ?token= query param (EventSource can't set headers)
	r.Get("/api/workspaces/{id}/provision-logs", h.ProvisionLogs)

	// Built-in WireGuard coordination — token auth via ?token= query param
	// Agents connect with role=agent, CLI clients with role=client.
	r.Get("/api/workspaces/{id}/tailnet", h.TailnetWS)

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

// BootstrapProvisionerKey creates a provisioner key from IGLOO_PROVISIONER_BOOTSTRAP_KEY
// if set. Safe to call on every startup — silently skips if the key already exists.
func (s *Server) BootstrapProvisionerKey(ctx context.Context) {
	raw := os.Getenv("IGLOO_PROVISIONER_BOOTSTRAP_KEY")
	if raw == "" {
		return
	}
	hash := api.HashToken(raw)
	if _, err := s.handler.Store.GetProvisionerKeyByTokenHash(ctx, hash); err == nil {
		return // already registered
	}
	k := &models.ProvisionerKey{
		ID:        "bootstrap",
		Name:      "bootstrap",
		TokenHash: hash,
		CreatedBy: "system",
		CreatedAt: time.Now(),
	}
	if err := s.handler.Store.CreateProvisionerKey(ctx, k); err != nil {
		log.Printf("[server] bootstrap provisioner key: %v", err)
		return
	}
	log.Printf("[server] bootstrap provisioner key registered")
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

func (s *Server) StartWorker(ctx context.Context) {
	// Dispatch loop runs in all modes — picks up pending jobs when a provisioner
	// reconnects after a server restart or transient DynamoDB GSI consistency lag.
	s.handler.Registry.StartDispatchLoop(ctx, s.handler)

	if os.Getenv("IGLOO_DAEMON_MODE") == "true" {
		// External provisioner daemon handles jobs; server only watches for dead daemons.
		go s.runHeartbeatWatcher(ctx)
		log.Printf("[server] provisioner daemon mode: heartbeat watcher started")
		return
	}
	s.handler.StartWorker(ctx)
}

// runHeartbeatWatcher re-queues daemon jobs that stop sending pings (daemon crashed).
func (s *Server) runHeartbeatWatcher(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stale, err := s.handler.Store.ListStaleDaemonJobs(ctx, 5*time.Minute)
			if err != nil {
				log.Printf("[heartbeat-watcher] list error: %v", err)
				continue
			}
			for _, job := range stale {
				if job.RetryCount >= models.JobMaxRetries {
					job.Status = models.JobFailed
					job.Error = "provisioner daemon lost contact"
					log.Printf("[heartbeat-watcher] job %s permanently failed (daemon silent)", job.ID)
					if ws, err := s.handler.Store.GetWorkspace(ctx, job.WorkspaceID); err == nil {
						ws.Status = models.StatusError
						ws.UpdatedAt = time.Now()
						_ = s.handler.Store.UpdateWorkspace(ctx, ws)
					}
				} else {
					job.RetryCount++
					job.Status = models.JobPending
					log.Printf("[heartbeat-watcher] re-queuing job %s (daemon silent, retry %d)", job.ID, job.RetryCount)
				}
				job.UpdatedAt = time.Now()
				_ = s.handler.Store.UpdateJob(ctx, job)
				if job.Status == models.JobPending {
					s.handler.TryImmediateDispatch(ctx)
				}
			}
		}
	}
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
