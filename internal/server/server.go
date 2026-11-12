package server

import (
	"context"
	"embed"
	"io"
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
		NetMap:      api.NewNetMapStore(),
	}
	h.Relay.OnAgentMessage(h.NetMap.HandleAgentMessage)
	// Build logs and network snapshots follow the store when it can keep them, so the
	// SSE stream and the Network tab work on a replica other than the one the
	// provisioner or agent reports to.
	if ls, ok := st.(api.LogStore); ok {
		h.LogBuf.Back(ls)
	}
	if ss, ok := st.(api.SnapshotStore); ok {
		h.NetMap.Back(ss)
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
			GroupsClaim:  os.Getenv("IGLOO_OIDC_GROUPS_CLAIM"), // default: "groups"
			RolesClaim:   os.Getenv("IGLOO_OIDC_ROLES_CLAIM"),  // default: "roles"
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
	h.AllowSignup = os.Getenv("IGLOO_ALLOW_SIGNUP") == "true"
	h.GuardKey = os.Getenv("IGLOO_GUARD_KEY")
	h.GuardProxyURL = os.Getenv("IGLOO_GUARD_PROXY_URL")
	h.SandboxImage = os.Getenv("IGLOO_SANDBOX_IMAGE")
	h.SandboxRuntimeClass = os.Getenv("IGLOO_SANDBOX_RUNTIME_CLASS")
	if raw := os.Getenv("IGLOO_OIDC_ALLOWED_GROUPS"); raw != "" {
		h.AllowedGroups = strings.Fields(strings.ReplaceAll(raw, ",", " "))
	}

	ui, uiDev := staticFS()

	r := chi.NewRouter()
	r.Use(redactLogURI) // keep ?token= and similar credentials out of the access log
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))
	r.Use(corsMiddleware(parseOrigins(os.Getenv("IGLOO_CORS_ORIGINS"))))

	// ─── Public routes ────────────────────────────────────────────────────────
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/downloads/{binary}", h.DownloadAgent)
	r.Get("/cli-auth", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		f, err := ui.Open("cli-auth.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		io.Copy(w, f)
	})
	r.Get("/api/auth/config", h.AuthConfig)
	r.Post("/api/auth/register", h.Register)
	r.Post("/api/auth/login", h.Login)
	r.Get("/api/auth/oidc/login", h.OIDCLogin)
	r.Get("/api/auth/oidc/callback", h.OIDCCallback)
	r.Route("/api/auth/device", func(r chi.Router) {
		r.Post("/init", h.DeviceInit)
		r.Get("/poll", h.DevicePoll)
		r.With(h.RequireAuth).Post("/approve", h.DeviceApprove)
	})
	r.Get("/ws", h.WebSocket)

	// ─── Authenticated routes ─────────────────────────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(h.RequireAuth)

		r.Get("/api/auth/me", h.Me)
		r.Post("/api/auth/password", h.ChangePassword)
		r.Put("/api/auth/me/tailscale-key", h.UpdateTailscaleKey)

		// API tokens (Coder-style service account tokens)
		r.Route("/api/tokens", func(r chi.Router) {
			r.Post("/", h.CreateToken)
			r.Get("/", h.ListTokens)
			r.Delete("/{id}", h.RevokeToken)
		})

		// Workspace + template routes — org context injected when X-Org-ID header is present.
		r.Group(func(r chi.Router) {
			r.Use(h.RequireOrgMember)

			r.Get("/api/workspaces", h.ListWorkspaces)
			r.Post("/api/workspaces", h.CreateWorkspace)
			r.Get("/api/workspaces/{id}", h.GetWorkspace)
			r.Post("/api/workspaces/{id}/start", h.StartWorkspace)
			r.Post("/api/workspaces/{id}/stop", h.StopWorkspace)
			r.Delete("/api/workspaces/{id}", h.DeleteWorkspace)
			r.Get("/api/workspaces/{id}/terminal", h.TerminalToken)
			r.Get("/api/workspaces/{id}/logs", h.WorkspaceLogs)
			r.Get("/api/workspaces/{id}/agent-logs", h.AgentLogs)
			r.Get("/api/workspaces/{id}/network", h.GetNetworkMap)
			r.Get("/api/workspaces/{id}/vscode-token", h.VScodeToken)
			r.Get("/api/workspaces/{id}/forward-token", h.ForwardToken)
			r.Get("/api/workspaces/{id}/logs-ticket", h.LogsTicket)
			r.Get("/api/workspaces/{id}/activity", h.WorkspaceActivity)

			// Sandboxes: workspaces made for code, driven through a small API.
			r.Post("/api/sandboxes", h.CreateSandbox)
			r.Get("/api/sandboxes/{id}", h.GetWorkspace)
			r.Delete("/api/sandboxes/{id}", h.DeleteWorkspace)
			r.Post("/api/sandboxes/{id}/exec", h.SandboxExec)
			r.Get("/api/sandboxes/{id}/files", h.SandboxReadFile)
			r.Put("/api/sandboxes/{id}/files", h.SandboxWriteFile)
			r.Get("/api/guard-baseline", h.GetGuardBaseline)
			r.Get("/api/workspaces/{id}/guard-policy", h.GetGuardPolicy)
			r.Put("/api/workspaces/{id}/guard-policy", h.PutGuardPolicy)

			r.Get("/api/templates", h.ListTemplates)
			r.Get("/api/templates/{id}", h.GetTemplate)
			r.Get("/api/templates/{id}/files", h.GetTemplateFiles)
			r.Post("/api/templates", h.CreateTemplate)
			r.Put("/api/templates/{id}", h.UpdateTemplate)
			r.Delete("/api/templates/{id}", h.DeleteTemplate)
		})

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
			r.Post("/api/admin/users", h.AdminCreateUser)
			r.Put("/api/admin/users/{id}", h.AdminUpdateUser)
			r.Put("/api/admin/users/{id}/password", h.AdminResetPassword)
			r.Delete("/api/admin/users/{id}", h.AdminDeleteUser)
			r.Get("/api/admin/workspaces", h.AdminListWorkspaces)
			r.Post("/api/admin/provisioner-keys", h.CreateProvisionerKey)
			r.Get("/api/admin/provisioner-keys", h.ListProvisionerKeys)
			r.Delete("/api/admin/provisioner-keys/{id}", h.DeleteProvisionerKey)
			r.Get("/api/admin/platform-config", h.GetPlatformConfig)
			r.Put("/api/admin/platform-config", h.SavePlatformConfig)
			r.Put("/api/admin/guard-baseline", h.PutGuardBaseline)
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

	// Egress gateway API (guard-key authenticated): who a token is, and the audit trail.
	r.Group(func(r chi.Router) {
		r.Use(h.GuardAuth)
		r.Post("/api/guard/authorize", h.GuardAuthorize)
		r.Post("/api/guard/events", h.GuardEvents)
	})

	// SSE log stream — short-lived ?ticket= from /logs-ticket (EventSource can't set headers)
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
	r.Handle("/*", spaHandler(ui, uiDev))

	return &Server{handler: h, router: r}
}

// StartHA joins this replica to the relay cluster when IGLOO_INTERNAL_ADDR is set — the
// address (host:port) where peer replicas can reach this pod's internal listener, e.g.
// "$(POD_IP):8081" from the downward API. Unset, the server runs exactly as before. The
// internal listener is served on its own port and must never be exposed through the
// ingress; peers authenticate with a key derived from the shared JWT secret.
func (s *Server) StartHA(ctx context.Context) {
	addr := os.Getenv("IGLOO_INTERNAL_ADDR")
	if addr == "" {
		return
	}
	owners, ok := s.handler.Store.(relay.OwnerStore)
	if !ok {
		log.Fatalf("[ha] the %T store cannot record agent ownership; more than one replica needs the Postgres store", s.handler.Store)
	}
	replicaID := os.Getenv("IGLOO_REPLICA_ID")
	if replicaID == "" {
		replicaID, _ = os.Hostname()
	}
	s.handler.Relay.JoinCluster(ctx, relay.Cluster{
		ReplicaID: replicaID,
		PodAddr:   addr,
		Key:       s.handler.Auth.InternalKey(),
		Owners:    owners,
	})

	port := os.Getenv("IGLOO_INTERNAL_PORT")
	if port == "" {
		port = "8081"
	}
	mux := http.NewServeMux()
	mux.Handle("/internal/relay/", s.handler.Relay.InternalHandler())
	go func() {
		log.Printf("[ha] replica %s serving peers on :%s (advertised %s)", replicaID, port, addr)
		if err := http.ListenAndServe(":"+port, mux); err != nil {
			log.Fatalf("[ha] internal listener: %v", err)
		}
	}()
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
	if os.Getenv("IGLOO_SEED_TEMPLATES") != "true" {
		return
	}
	templates := []*models.Template{
		{
			ID:          "tpl-kubernetes",
			Name:        "Kubernetes",
			Description: "Workspace pod on the Kubernetes cluster with persistent EBS disk. Sleeps to $0 — disk preserved.",
			Provider:    "kubernetes",
			Icon:        "☸️",
			Builtin:     true,
			Variables: []models.TemplateVar{
				{Name: "workspace_image", DisplayName: "Container Image", Description: "Any Docker image — the Igloo agent is injected automatically at startup.", Default: "ubuntu:22.04", Type: "string", Mutable: false},
				{Name: "cpu", DisplayName: "CPU", Description: "CPU request for the workspace container.", Default: "500m", Type: "select", Options: []string{"500m", "1000m", "2000m"}, Mutable: true},
				{Name: "memory", DisplayName: "Memory", Description: "Memory request for the workspace container.", Default: "512Mi", Type: "select", Options: []string{"512Mi", "1Gi", "2Gi"}, Mutable: true},
				{Name: "disk", DisplayName: "Disk", Description: "Persistent volume size for the workspace home directory.", Default: "10Gi", Type: "select", Options: []string{"10Gi", "20Gi", "50Gi"}, Mutable: false},
				{Name: "runtime_class", DisplayName: "Runtime", Description: "Kubernetes RuntimeClass for the pod. Name a microVM runtime such as \"kata\" (nodes must have it installed) for a workspace with its own kernel; empty uses the cluster default.", Default: "", Type: "string", Mutable: false},
				{Name: "gpu", DisplayName: "GPUs", Description: "NVIDIA GPUs to reserve (nodes need the device plugin). 0 for none.", Default: "0", Type: "string", Mutable: false},
			},
			Files: loadTemplateFiles("kubernetes"),
		},
		{
			ID:          "tpl-docker-terraform",
			Name:        "Docker (Terraform)",
			Description: "Same as Docker but provisioned via Terraform. Use this to test the Terraform workflow locally.",
			Provider:    "docker-terraform",
			Icon:        "🐳",
			Builtin:     true,
			Variables:   []models.TemplateVar{},
			Files:       loadTemplateFiles("docker-terraform"),
		},
	}

	for _, t := range templates {
		now := time.Now()
		t.CreatedAt = now
		t.UpdatedAt = now
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
	s.handler.StartSandboxJanitor(ctx, 30*time.Second)

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

// parseOrigins turns a comma-separated list into a set. Empty means same-origin only.
func parseOrigins(raw string) map[string]bool {
	out := map[string]bool{}
	for _, o := range strings.Split(raw, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out[o] = true
		}
	}
	return out
}

// corsMiddleware allows cross-origin browser calls only from listed origins
// (IGLOO_CORS_ORIGINS, or "*" to allow any). The bundled UI is same-origin and
// the CLI is not a browser, so the default is to send no CORS headers at all.
func corsMiddleware(allowed map[string]bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if origin := r.Header.Get("Origin"); origin != "" && (allowed["*"] || allowed[origin]) {
				if allowed["*"] {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				} else {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Add("Vary", "Origin")
				}
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Org-ID")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// staticFS returns the UI files. When IGLOO_UI_DIR is set the UI is served from
// that directory on disk (edits show up on refresh, no rebuild); otherwise the
// copy embedded in the binary is used.
func staticFS() (fs.FS, bool) {
	if dir := os.Getenv("IGLOO_UI_DIR"); dir != "" {
		log.Printf("[server] serving UI from %s (live reload mode)", dir)
		return os.DirFS(dir), true
	}
	sub, err := fs.Sub(uiFS, "static")
	if err != nil {
		panic(err)
	}
	return sub, false
}

// spaHandler serves static files and falls back to index.html for SPA client-side routing.
// With noCache set, browsers always revalidate (used in live reload mode).
func spaHandler(static fs.FS, noCache bool) http.Handler {
	fileServer := http.FileServer(http.FS(static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noCache {
			w.Header().Set("Cache-Control", "no-store")
		}
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
