package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/guard"
	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/worker"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// GET /api/workspaces?q=owner:me+template:kubernetes+status:running
func (h *Handler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	f := ParseWorkspaceFilter(r.URL.Query().Get("q"))
	callerID := ctxUserID(r)

	// Always return only the current user's workspaces.
	// Admins who need all workspaces use GET /api/admin/workspaces.
	ownerID := callerID

	ws, err := h.Store.ListWorkspaces(r.Context(), ownerID, ctxOrgID(r))
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Build template name index for filtering.
	tplNames := h.templateNameIndex(r.Context())
	ws = applyWorkspaceFilter(ws, f, tplNames)

	if ws == nil {
		ws = []*models.Workspace{}
	}
	h.json(w, http.StatusOK, ws)
}

// createInput is everything needed to make a workspace.
type createInput struct {
	Name       string
	TemplateID string
	Vars       map[string]string
	Guard      *guard.Policy     // nil = not behind the egress gateway
	Meta       map[string]string // extra metadata (e.g. sandbox marker and expiry)
}

// createWorkspace checks in, stores a new workspace owned by the caller and queues its
// provisioning. On failure it returns the HTTP status and message to report.
func (h *Handler) createWorkspace(r *http.Request, in createInput) (*models.Workspace, int, string) {
	base, err := h.baseline(r.Context())
	if err != nil {
		return nil, http.StatusInternalServerError, "internal error"
	}
	if in.Guard == nil && base.RequireGuard {
		return nil, http.StatusForbidden, "your organisation requires workspaces to run behind the egress gateway; give a guard policy"
	}
	if in.Guard != nil {
		if err := in.Guard.Validate(); err != nil {
			return nil, http.StatusBadRequest, err.Error()
		}
		if err := base.Check(*in.Guard); err != nil {
			return nil, http.StatusForbidden, err.Error()
		}
		if !h.guardEnabled() {
			return nil, http.StatusConflict, "the egress gateway is not configured on this server"
		}
	}

	tmpl, err := h.Store.GetTemplate(r.Context(), in.TemplateID)
	if err != nil {
		return nil, http.StatusNotFound, "template not found"
	}

	// Merge defaults then user-supplied vars
	vars := make(map[string]string)
	for _, v := range tmpl.Variables {
		if v.Default != "" {
			vars[v.Name] = v.Default
		}
	}
	for k, v := range in.Vars {
		vars[k] = v
	}

	orgID := ctxOrgID(r)
	// Enforce template belongs to this org (or is builtin).
	if orgID != "" && !tmpl.Builtin && tmpl.OrgID != orgID {
		return nil, http.StatusForbidden, "template does not belong to this organization"
	}

	wsID := uuid.NewString()
	agentToken, err := h.Auth.SignConn(wsID, "agent", 30*24*time.Hour)
	if err != nil {
		return nil, http.StatusInternalServerError, "internal error"
	}

	ws := &models.Workspace{
		ID:           wsID,
		OrgID:        orgID,
		UserID:       ctxUserID(r),
		Name:         strings.TrimSpace(in.Name),
		TemplateID:   tmpl.ID,
		TemplateName: tmpl.Name,
		Provider:     tmpl.Provider,
		Status:       models.StatusCreating,
		AgentToken:   agentToken,
		Vars:         vars,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		LastActiveAt: time.Now(),
		Meta:         map[string]string{},
	}
	for k, v := range in.Meta {
		ws.Meta[k] = v
	}
	if in.Guard != nil {
		setPolicy(ws, *in.Guard)
	}

	if err := h.Store.CreateWorkspace(r.Context(), ws); err != nil {
		return nil, http.StatusInternalServerError, "internal error"
	}

	// Auto-start: enqueue the provisioning job immediately.
	if err := worker.Enqueue(r.Context(), h.Store, ws.ID, models.JobStart); err != nil {
		log.Printf("[workspace] failed to enqueue start job workspace=%s: %v", ws.ID, err)
	} else {
		h.TryImmediateDispatch(r.Context())
	}
	return ws, 0, ""
}

// POST /api/workspaces
func (h *Handler) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string            `json:"name"`
		TemplateID string            `json:"template_id"`
		Vars       map[string]string `json:"vars"`
		Guard      *guard.Policy     `json:"guard"` // optional: run behind the egress gateway with this policy
	}
	if err := h.decode(r, &body); err != nil || body.Name == "" || body.TemplateID == "" {
		h.err(w, http.StatusBadRequest, "name and template_id are required")
		return
	}
	ws, code, msg := h.createWorkspace(r, createInput{Name: body.Name, TemplateID: body.TemplateID, Vars: body.Vars, Guard: body.Guard})
	if code != 0 {
		h.err(w, code, msg)
		return
	}
	h.json(w, http.StatusCreated, ws)
}

// GET /api/workspaces/{id}
func (h *Handler) GetWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	if !h.canAccessWorkspace(r, ws) {
		h.err(w, http.StatusForbidden, "forbidden")
		return
	}
	h.json(w, http.StatusOK, ws)
}

// POST /api/workspaces/{id}/start
func (h *Handler) StartWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || !h.canAccessWorkspace(r, ws) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	if ws.Status == models.StatusRunning || ws.Status == models.StatusCreating {
		h.json(w, http.StatusOK, ws)
		return
	}
	if ws.Status == models.StatusDeleting {
		h.err(w, http.StatusConflict, "workspace is being deleted")
		return
	}
	// A workspace made before the baseline required the gateway cannot be moved behind it, so
	// it stays stopped rather than running outside the organisation's rules.
	if _, guarded := policyOf(ws); !guarded {
		base, err := h.baseline(r.Context())
		if err != nil {
			h.err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if base.RequireGuard {
			h.err(w, http.StatusForbidden, "your organisation requires workspaces to run behind the egress gateway, and this one was created without it; create a new one with a guard policy")
			return
		}
	}

	agentToken, err := h.Auth.SignConn(ws.ID, "agent", 30*24*time.Hour)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	ws.AgentToken = agentToken
	ws.Status = models.StatusCreating
	ws.UpdatedAt = time.Now()
	if err := h.Store.UpdateWorkspace(r.Context(), ws); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	if ws.Provider == "aws-fargate" || ws.Provider == "kubernetes" {
		// Inject Tailscale: platform API key takes priority (admin configures once),
		// then fall back to the user's personal auth key.
		var tsKey, tsHostname string
		if cfg, _ := h.Store.GetPlatformConfig(r.Context()); cfg != nil {
			tsKey, tsHostname, _ = IssueTailscaleKey(cfg.TailscaleAPIKey, cfg.TailscaleTailnet, ws.ID, ws.Name)
		}
		if tsKey == "" {
			if user, err := h.Store.GetUser(r.Context(), ws.UserID); err == nil && user.TailscaleAuthKey != "" {
				tsKey = user.TailscaleAuthKey
				tsHostname = tailscaleHostname(ws.Name, ws.ID)
			}
		}
		if tsKey != "" {
			if ws.Vars == nil {
				ws.Vars = make(map[string]string)
			}
			ws.Vars["tailscale_auth_key"] = tsKey
			ws.Vars["tailscale_hostname"] = tsHostname
			_ = h.Store.UpdateWorkspace(r.Context(), ws)
			log.Printf("[workspace] tailscale injected hostname=%s workspace=%s", tsHostname, ws.ID)
		}

		if err := worker.Enqueue(r.Context(), h.Store, ws.ID, models.JobStart); err != nil {
			log.Printf("[workspace] failed to enqueue start job workspace=%s: %v", ws.ID, err)
		} else {
			h.TryImmediateDispatch(r.Context())
		}
	} else {
		tmpl, _ := h.Store.GetTemplate(r.Context(), ws.TemplateID)
		serverURL := serverURLFromRequest(r)
		go h.launchWorkspace(ws, tmpl, serverURL)
	}

	h.json(w, http.StatusOK, ws)
}

// POST /api/workspaces/{id}/stop
func (h *Handler) StopWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || !h.canAccessWorkspace(r, ws) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	if ws.Status == models.StatusDeleting {
		h.err(w, http.StatusConflict, "workspace is being deleted")
		return
	}

	if ws.Provider == "aws-fargate" || ws.Provider == "kubernetes" {
		if err := worker.Enqueue(r.Context(), h.Store, ws.ID, models.JobStop); err != nil {
			log.Printf("[workspace] failed to enqueue stop job workspace=%s: %v", ws.ID, err)
		} else {
			h.TryImmediateDispatch(r.Context())
		}
	} else {
		tmpl, _ := h.Store.GetTemplate(r.Context(), ws.TemplateID)
		go h.stopWorkspace(ws, tmpl)
	}

	ws.Status = models.StatusSleeping
	ws.UpdatedAt = time.Now()
	_ = h.Store.UpdateWorkspace(r.Context(), ws)
	h.json(w, http.StatusOK, ws)
}

// DELETE /api/workspaces/{id}
func (h *Handler) DeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || !h.canAccessWorkspace(r, ws) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	h.beginDelete(r.Context(), ws)
	w.WriteHeader(http.StatusNoContent)
}

// beginDelete starts tearing a workspace down. It is safe to call twice.
func (h *Handler) beginDelete(ctx context.Context, ws *models.Workspace) {
	if ws.Status == models.StatusDeleting {
		return // already in progress
	}
	if ws.Provider == "aws-fargate" || ws.Provider == "kubernetes" {
		ws.Status = models.StatusDeleting
		ws.UpdatedAt = time.Now()
		_ = h.Store.UpdateWorkspace(ctx, ws)
		if err := worker.Enqueue(ctx, h.Store, ws.ID, models.JobDestroy); err != nil {
			log.Printf("[workspace] failed to enqueue destroy job workspace=%s: %v", ws.ID, err)
		} else {
			h.TryImmediateDispatch(ctx)
		}
		return
	}
	tmpl, _ := h.Store.GetTemplate(ctx, ws.TemplateID)
	go h.destroyWorkspace(ws, tmpl)
	_ = h.Store.DeleteWorkspace(ctx, ws.ID)
}

// GET /api/workspaces/{id}/provision-logs — SSE stream of live provisioner output.
// Auth is either an Authorization header (scripts) or ?ticket=, a 30-second token
// from /logs-ticket (EventSource can't set headers). Session tokens are not
// accepted in the query string.
func (h *Handler) ProvisionLogs(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "id")
	var userID string // set for header auth; a ticket was already scoped to this workspace when issued
	if ticket := r.URL.Query().Get("ticket"); ticket != "" {
		claims, err := h.Auth.ParseConn(ticket)
		if err != nil || claims.Role != logsTicketRole || claims.WorkspaceID != wsID {
			h.err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
	} else {
		bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		claims, err := h.Auth.ParseUser(bearer)
		if err != nil || h.sessionRevoked(r.Context(), claims) {
			h.err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		userID = claims.UserID
	}
	ws, err := h.Store.GetWorkspace(r.Context(), wsID)
	if err != nil || (userID != "" && ws.UserID != userID) {
		h.err(w, http.StatusNotFound, "not found")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.err(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	snapshot, ch, cancel := h.LogBuf.Subscribe(wsID)
	defer cancel()

	for _, line := range snapshot {
		fmt.Fprintf(w, "data: %s\n\n", line)
	}
	flusher.Flush()

	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// GET /api/workspaces/{id}/terminal
func (h *Handler) TerminalToken(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || !h.canAccessWorkspace(r, ws) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	if ws.Status != models.StatusRunning {
		h.err(w, http.StatusConflict, fmt.Sprintf("workspace is %s", ws.Status))
		return
	}
	token, err := h.Auth.SignConn(ws.ID, "browser", time.Hour)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, map[string]string{
		"token":  token,
		"ws_url": serverURLFromRequest(r) + "/ws",
	})
}

// GET /api/workspaces/{id}/agent-logs
func (h *Handler) AgentLogs(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || !h.canAccessWorkspace(r, ws) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}

	if ws.Provider == "aws-fargate" {
		text, err := h.cloudwatchLogs(ws.ID)
		if err != nil {
			h.err(w, http.StatusNotFound, "logs not available yet")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte(text))
		return
	}

	if ws.Provider == "kubernetes" {
		text, err := k8sPodLogs(r.Context(), ws.ID)
		if err != nil {
			log.Printf("[agent-logs] kubernetes workspace=%s: %v", ws.ID, err)
			h.err(w, http.StatusNotFound, "logs not available yet")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte(text))
		return
	}

	var containerName string
	switch ws.Provider {
	case "docker":
		containerName = "igloo-" + ws.ID[:8]
	case "docker-terraform":
		containerName = "igloo-tf-" + ws.ID
	default:
		if id := ws.Meta["container_id"]; id != "" {
			containerName = id
		}
	}
	if containerName == "" {
		h.err(w, http.StatusNotFound, "no container for this workspace")
		return
	}
	out, err := exec.Command("docker", "logs", "--timestamps", containerName).CombinedOutput()
	if err != nil && len(out) == 0 {
		h.err(w, http.StatusNotFound, "container not found or no logs yet")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(out)
}

// GET /ws
func (h *Handler) WebSocket(w http.ResponseWriter, r *http.Request) {
	token := connToken(r)
	claims, err := h.Auth.ParseConn(token)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	if claims.Role == "agent" {
		conn.SetReadLimit(maxAgentFrame)
	} else {
		conn.SetReadLimit(maxBrowserFrame)
	}

	connID := uuid.NewString()
	ctx := context.Background()

	switch claims.Role {
	case "agent":
		h.Relay.RegisterAgentNotify(claims.WorkspaceID, connID, conn, func() {
			// Flip to running only now that the relay can reach the agent.
			if cur, e := h.Store.GetWorkspace(ctx, claims.WorkspaceID); e == nil {
				// Don't overwrite deleting status — agent may still be alive during terraform destroy
				if cur.Status != models.StatusDeleting {
					cur.Status = models.StatusRunning
					cur.UpdatedAt = time.Now()
					cur.LastActiveAt = time.Now()
					_ = h.Store.UpdateWorkspace(ctx, cur)
				}
			}
		})
		// This connection ended. Mark the workspace asleep only if no agent is left:
		// after a network blip the new connection registers before the old one closes.
		if !h.Relay.AgentConnected(claims.WorkspaceID) {
			if cur, e := h.Store.GetWorkspace(ctx, claims.WorkspaceID); e == nil && cur.Status == models.StatusRunning {
				cur.Status = models.StatusSleeping
				cur.UpdatedAt = time.Now()
				_ = h.Store.UpdateWorkspace(ctx, cur)
			}
		}

	case "browser":
		h.Relay.RegisterBrowser(claims.WorkspaceID, connID, conn)

	default:
		conn.Close()
	}
}

// ─── Launch dispatch ──────────────────────────────────────────────────────────

func (h *Handler) launchWorkspace(ws *models.Workspace, tmpl *models.Template, serverURL string) {
	var err error
	switch ws.Provider {
	case "docker":
		err = h.launchDocker(ws, serverURL)
	case "docker-terraform":
		err = h.launchTerraform(ws, serverURL, "docker")
	default:
		err = fmt.Errorf("unsupported provider: %s", ws.Provider)
	}
	if err != nil {
		log.Printf("[workspace] launch failed workspace=%s: %v", ws.ID, err)
		if cur, e := h.Store.GetWorkspace(context.Background(), ws.ID); e == nil {
			cur.Status = models.StatusError
			cur.UpdatedAt = time.Now()
			_ = h.Store.UpdateWorkspace(context.Background(), cur)
		}
	}
}

func (h *Handler) stopWorkspace(ws *models.Workspace, tmpl *models.Template) {
	switch ws.Provider {
	case "docker":
		if id := ws.Meta["container_id"]; id != "" {
			exec.Command("docker", "stop", id).Run()
		}
	case "docker-terraform":
		if dir := ws.Meta["tf_dir"]; dir != "" {
			h.terraformDestroy(dir)
		}
	}
}

func (h *Handler) destroyWorkspace(ws *models.Workspace, tmpl *models.Template) {
	switch ws.Provider {
	case "docker":
		if id := ws.Meta["container_id"]; id != "" {
			exec.Command("docker", "stop", id).Run()
		}
	case "docker-terraform":
		if dir := ws.Meta["tf_dir"]; dir != "" {
			h.terraformDestroy(dir)
		}
	}
}

// ─── Docker launch ────────────────────────────────────────────────────────────

func (h *Handler) launchDocker(ws *models.Workspace, serverURL string) error {
	dockerURL := strings.NewReplacer(
		"://localhost", "://host.docker.internal",
		"://127.0.0.1", "://host.docker.internal",
	).Replace(serverURL)

	args := []string{
		"run", "-d", "--rm",
		"--name", "igloo-" + ws.ID[:8],
		"--add-host=host.docker.internal:host-gateway",
		"-e", "IGLOO_SERVER_URL=" + dockerURL,
		"-e", "IGLOO_AGENT_TOKEN=" + ws.AgentToken,
		"-e", "IGLOO_WORKSPACE_ID=" + ws.ID,
		"igloo-workspace:latest",
	}

	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker run: %w — %s", err, strings.TrimSpace(string(out)))
	}

	containerID := strings.TrimSpace(string(out))
	if cur, e := h.Store.GetWorkspace(context.Background(), ws.ID); e == nil {
		if cur.Meta == nil {
			cur.Meta = map[string]string{}
		}
		cur.Meta["container_id"] = containerID
		_ = h.Store.UpdateWorkspace(context.Background(), cur)
	}
	return nil
}

// ─── Terraform (docker-terraform provider) ───────────────────────────────────

func (h *Handler) launchTerraform(ws *models.Workspace, serverURL, templateName string) error {
	srcDir, err := filepath.Abs(filepath.Join("templates", templateName))
	if err != nil {
		return err
	}
	if _, err := os.Stat(srcDir); err != nil {
		return fmt.Errorf("template %q not found at %s", templateName, srcDir)
	}

	home, _ := os.UserHomeDir()
	wsDir := filepath.Join(home, ".igloo", "workspaces", ws.ID)
	if err := os.MkdirAll(wsDir, 0700); err != nil {
		return err
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(srcDir, e.Name()))
		os.WriteFile(filepath.Join(wsDir, e.Name()), data, 0644)
	}

	dockerURL := strings.NewReplacer(
		"://localhost", "://host.docker.internal",
		"://127.0.0.1", "://host.docker.internal",
	).Replace(serverURL)

	tfvars := fmt.Sprintf("workspace_id = %q\nserver_url = %q\nagent_token = %q\n",
		ws.ID, dockerURL, ws.AgentToken)
	os.WriteFile(filepath.Join(wsDir, "terraform.tfvars"), []byte(tfvars), 0600)

	logPath := filepath.Join(wsDir, "terraform.log")
	if cur, e := h.Store.GetWorkspace(context.Background(), ws.ID); e == nil {
		if cur.Meta == nil {
			cur.Meta = map[string]string{}
		}
		cur.Meta["tf_dir"] = wsDir
		cur.Meta["log_path"] = logPath
		_ = h.Store.UpdateWorkspace(context.Background(), cur)
	}

	lf, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	defer lf.Close()

	initCmd := h.tfCmd(wsDir, "init", "-input=false", "-no-color")
	initCmd.Stdout, initCmd.Stderr = lf, lf
	if err := initCmd.Run(); err != nil {
		return fmt.Errorf("terraform init failed (see logs)")
	}

	applyCmd := h.tfCmd(wsDir, "apply", "-auto-approve", "-input=false", "-no-color")
	applyCmd.Stdout, applyCmd.Stderr = lf, lf
	if err := applyCmd.Run(); err != nil {
		return fmt.Errorf("terraform apply failed (see logs)")
	}
	return nil
}

func (h *Handler) terraformDestroy(wsDir string) {
	logPath := filepath.Join(wsDir, "terraform.log")
	lf, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
	cmd := h.tfCmd(wsDir, "destroy", "-auto-approve", "-input=false", "-no-color")
	if lf != nil {
		fmt.Fprintln(lf, "\n[terraform] destroy")
		cmd.Stdout, cmd.Stderr = lf, lf
	}
	cmd.Run()
	if lf != nil {
		lf.Close()
	}
	os.RemoveAll(wsDir)
}

func (h *Handler) tfCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("terraform", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1")
	return cmd
}

// GET /api/workspaces/{id}/logs
func (h *Handler) WorkspaceLogs(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || !h.canAccessWorkspace(r, ws) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}

	if ws.Provider == "aws-fargate" {
		var buf strings.Builder
		if lp := ws.Meta["log_path"]; lp != "" {
			if data, err := os.ReadFile(lp); err == nil {
				buf.Write(data)
				buf.WriteString("\n--- container logs ---\n")
			}
		}
		if text, err := h.cloudwatchLogs(ws.ID); err == nil && text != "" {
			buf.WriteString(text)
		}
		if buf.Len() == 0 {
			h.err(w, http.StatusNotFound, "no logs yet")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte(buf.String()))
		return
	}

	// Daemon-provisioned workspaces stream Terraform output to the server, which
	// buffers it in memory; there is no log file on this host.
	if lines := h.LogBuf.Snapshot(ws.ID); len(lines) > 0 {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write([]byte(strings.Join(lines, "\n") + "\n"))
		return
	}

	logPath := ws.Meta["log_path"]
	if logPath == "" {
		h.err(w, http.StatusNotFound, "no logs for this workspace")
		return
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		h.err(w, http.StatusNotFound, "log file not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

// cloudwatchLogs fetches container logs from CloudWatch using the workspace ID
// as the log stream name prefix (set by awslogs-stream-prefix in task def).
func (h *Handler) cloudwatchLogs(workspaceID string) (string, error) {
	logGroup := "/igloo/workspaces"

	cfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return "", err
	}
	client := cloudwatchlogs.NewFromConfig(cfg)

	var buf strings.Builder
	var nextToken *string
	for {
		out, err := client.FilterLogEvents(context.Background(), &cloudwatchlogs.FilterLogEventsInput{
			LogGroupName:        &logGroup,
			LogStreamNamePrefix: &workspaceID,
			NextToken:           nextToken,
		})
		if err != nil {
			return "", err
		}
		for _, e := range out.Events {
			buf.WriteString(aws.ToString(e.Message))
			buf.WriteByte('\n')
		}
		if out.NextToken == nil {
			break
		}
		if nextToken != nil && aws.ToString(out.NextToken) == aws.ToString(nextToken) {
			break
		}
		nextToken = out.NextToken
	}
	return buf.String(), nil
}

func serverURLFromRequest(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		return fwd + "://" + r.Host
	}
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}
