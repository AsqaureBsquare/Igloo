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
	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/provisioner"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// GET /api/workspaces
func (h *Handler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.ListWorkspaces(r.Context(), ctxUserID(r))
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	if ws == nil {
		ws = []*models.Workspace{}
	}
	h.json(w, http.StatusOK, ws)
}

// POST /api/workspaces
func (h *Handler) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string            `json:"name"`
		TemplateID string            `json:"template_id"`
		Vars       map[string]string `json:"vars"`
	}
	if err := h.decode(r, &body); err != nil || body.Name == "" || body.TemplateID == "" {
		h.err(w, http.StatusBadRequest, "name and template_id are required")
		return
	}

	tmpl, err := h.Store.GetTemplate(r.Context(), body.TemplateID)
	if err != nil {
		h.err(w, http.StatusNotFound, "template not found")
		return
	}

	// Merge defaults then user-supplied vars
	vars := make(map[string]string)
	for _, v := range tmpl.Variables {
		if v.Default != "" {
			vars[v.Name] = v.Default
		}
	}
	for k, v := range body.Vars {
		vars[k] = v
	}

	ws := &models.Workspace{
		ID:           uuid.NewString(),
		UserID:       ctxUserID(r),
		Name:         strings.TrimSpace(body.Name),
		TemplateID:   tmpl.ID,
		TemplateName: tmpl.Name,
		Provider:     tmpl.Provider,
		Status:       models.StatusSleeping,
		Vars:         vars,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		LastActiveAt: time.Now(),
		Meta:         map[string]string{},
	}

	if err := h.Store.CreateWorkspace(r.Context(), ws); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
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
	if ws.UserID != ctxUserID(r) {
		h.err(w, http.StatusForbidden, "forbidden")
		return
	}
	h.json(w, http.StatusOK, ws)
}

// POST /api/workspaces/{id}/start
func (h *Handler) StartWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || ws.UserID != ctxUserID(r) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	if ws.Status == models.StatusRunning {
		h.json(w, http.StatusOK, ws)
		return
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

	sshKey := ""
	if user, err := h.Store.GetUser(r.Context(), ws.UserID); err == nil {
		sshKey = user.SSHPublicKey
	}

	tmpl, _ := h.Store.GetTemplate(r.Context(), ws.TemplateID)
	serverURL := serverURLFromRequest(r)
	go h.launchWorkspace(ws, tmpl, serverURL, sshKey)

	h.json(w, http.StatusOK, ws)
}

// POST /api/workspaces/{id}/stop
func (h *Handler) StopWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || ws.UserID != ctxUserID(r) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	tmpl, _ := h.Store.GetTemplate(r.Context(), ws.TemplateID)
	go h.stopWorkspace(ws, tmpl)
	ws.Status = models.StatusSleeping
	ws.UpdatedAt = time.Now()
	_ = h.Store.UpdateWorkspace(r.Context(), ws)
	h.json(w, http.StatusOK, ws)
}

// DELETE /api/workspaces/{id}
func (h *Handler) DeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || ws.UserID != ctxUserID(r) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	tmpl, _ := h.Store.GetTemplate(r.Context(), ws.TemplateID)
	go h.destroyWorkspace(ws, tmpl)
	_ = h.Store.DeleteWorkspace(r.Context(), ws.ID)
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/workspaces/{id}/terminal
func (h *Handler) TerminalToken(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || ws.UserID != ctxUserID(r) {
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
	if err != nil || ws.UserID != ctxUserID(r) {
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
	token := r.URL.Query().Get("token")
	claims, err := h.Auth.ParseConn(token)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	connID := uuid.NewString()
	ctx := context.Background()

	switch claims.Role {
	case "agent":
		if cur, e := h.Store.GetWorkspace(ctx, claims.WorkspaceID); e == nil {
			cur.Status = models.StatusRunning
			cur.UpdatedAt = time.Now()
			cur.LastActiveAt = time.Now()
			_ = h.Store.UpdateWorkspace(ctx, cur)
		}
		h.Relay.RegisterAgent(claims.WorkspaceID, connID, conn)
		if cur, e := h.Store.GetWorkspace(ctx, claims.WorkspaceID); e == nil && cur.Status == models.StatusRunning {
			cur.Status = models.StatusSleeping
			cur.UpdatedAt = time.Now()
			_ = h.Store.UpdateWorkspace(ctx, cur)
		}

	case "browser":
		h.Relay.RegisterBrowser(claims.WorkspaceID, connID, conn)

	default:
		conn.Close()
	}
}

// ─── Launch dispatch ──────────────────────────────────────────────────────────

func (h *Handler) launchWorkspace(ws *models.Workspace, tmpl *models.Template, serverURL, sshKey string) {
	var err error
	switch ws.Provider {
	case "docker":
		err = h.launchDocker(ws, serverURL)
	case "docker-terraform":
		err = h.launchTerraform(ws, serverURL, "docker")
	case "aws-fargate":
		err = h.launchFargate(ws, tmpl, serverURL, sshKey)
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
	case "aws-fargate":
		h.sleepFargate(ws, tmpl)
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
	case "aws-fargate":
		if tmpl != nil {
			h.destroyFargate(ws, tmpl)
		}
	}
}

// ─── Fargate (pure Terraform) ─────────────────────────────────────────────────

func (h *Handler) launchFargate(ws *models.Workspace, tmpl *models.Template, serverURL, sshKey string) error {
	if tmpl == nil {
		return fmt.Errorf("template not found")
	}
	logPath := h.provisionLogPath(ws.ID)
	if cur, e := h.Store.GetWorkspace(context.Background(), ws.ID); e == nil {
		cur.Meta["log_path"] = logPath
		_ = h.Store.UpdateWorkspace(context.Background(), cur)
	}

	files := tmpl.Files
	if len(files) == 0 {
		files = loadFilesFromDisk("aws-fargate")
	}
	if len(files) == 0 {
		return fmt.Errorf("no terraform files for aws-fargate template")
	}

	bucket := os.Getenv("IGLOO_TF_STATE_BUCKET")
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	if bucket == "" {
		return fmt.Errorf("IGLOO_TF_STATE_BUCKET not set")
	}

	vars := h.buildFargateVars(ws, serverURL, sshKey)
	vars["desired_count"] = "1"

	outputs, err := provisioner.Run(context.Background(), provisioner.Input{
		WorkspaceID: ws.ID,
		Files:       files,
		Vars:        vars,
		StateBucket: bucket,
		StateRegion: region,
		LogPath:     logPath,
	}, provisioner.ActionApply)
	if err != nil {
		return fmt.Errorf("terraform apply: %w", err)
	}

	if cur, e := h.Store.GetWorkspace(context.Background(), ws.ID); e == nil {
		if svc := outputs["service_name"]; svc != "" {
			cur.Meta["service_name"] = svc
		}
		_ = h.Store.UpdateWorkspace(context.Background(), cur)
	}

	return nil
}

func (h *Handler) sleepFargate(ws *models.Workspace, tmpl *models.Template) {
	if tmpl == nil {
		return
	}
	files := tmpl.Files
	if len(files) == 0 {
		files = loadFilesFromDisk("aws-fargate")
	}
	if len(files) == 0 {
		log.Printf("[fargate] no files to sleep workspace %s", ws.ID)
		return
	}

	bucket := os.Getenv("IGLOO_TF_STATE_BUCKET")
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	logPath := h.provisionLogPath(ws.ID)
	vars := h.buildFargateVars(ws, "", "")
	vars["desired_count"] = "0"

	if _, err := provisioner.Run(context.Background(), provisioner.Input{
		WorkspaceID: ws.ID,
		Files:       files,
		Vars:        vars,
		StateBucket: bucket,
		StateRegion: region,
		LogPath:     logPath,
	}, provisioner.ActionApply); err != nil {
		log.Printf("[fargate] sleep failed workspace=%s: %v", ws.ID, err)
	}
}

func (h *Handler) destroyFargate(ws *models.Workspace, tmpl *models.Template) {
	files := tmpl.Files
	if len(files) == 0 {
		files = loadFilesFromDisk("aws-fargate")
	}
	if len(files) == 0 {
		log.Printf("[fargate] no files to run terraform destroy for workspace %s", ws.ID)
		return
	}

	sshKey := ""
	if user, err := h.Store.GetUser(context.Background(), ws.UserID); err == nil {
		sshKey = user.SSHPublicKey
	}

	bucket := os.Getenv("IGLOO_TF_STATE_BUCKET")
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	logPath := h.provisionLogPath(ws.ID)
	vars := h.buildFargateVars(ws, "", sshKey)
	vars["desired_count"] = "0"

	if _, err := provisioner.Run(context.Background(), provisioner.Input{
		WorkspaceID: ws.ID,
		Files:       files,
		Vars:        vars,
		StateBucket: bucket,
		StateRegion: region,
		LogPath:     logPath,
	}, provisioner.ActionDestroy); err != nil {
		log.Printf("[fargate] terraform destroy failed workspace=%s: %v", ws.ID, err)
	}
}

func (h *Handler) buildFargateVars(ws *models.Workspace, serverURL, sshKey string) map[string]string {
	vars := make(map[string]string)
	for k, v := range ws.Vars {
		vars[k] = v
	}
	vars["workspace_id"] = ws.ID
	vars["workspace_name"] = ws.Name
	vars["server_url"] = serverURL
	vars["agent_token"] = ws.AgentToken
	vars["ssh_public_key"] = sshKey
	vars["cluster_name"] = os.Getenv("IGLOO_ECS_CLUSTER")
	vars["task_execution_role_arn"] = os.Getenv("IGLOO_ECS_EXECUTION_ROLE_ARN")
	vars["task_role_arn"] = os.Getenv("IGLOO_ECS_TASK_ROLE_ARN")
	vars["subnet_ids"] = os.Getenv("IGLOO_ECS_SUBNETS")
	vars["security_group_id"] = os.Getenv("IGLOO_ECS_SG")
	vars["workspace_image"] = os.Getenv("IGLOO_WORKSPACE_IMAGE")
	if vars["region"] == "" {
		if r := os.Getenv("AWS_REGION"); r != "" {
			vars["region"] = r
		} else {
			vars["region"] = "us-east-1"
		}
	}
	return vars
}

func (h *Handler) provisionLogPath(workspaceID string) string {
	dir := filepath.Join(os.TempDir(), "igloo-logs")
	_ = os.MkdirAll(dir, 0755)
	return filepath.Join(dir, workspaceID+".log")
}

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
	if err != nil || ws.UserID != ctxUserID(r) {
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
