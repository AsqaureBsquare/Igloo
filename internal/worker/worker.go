// Package worker provides the background job processor for workspace provisioning.
// It can run embedded in the API server (concurrency=1) or as a standalone
// provisioner service (concurrency=10).
package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/provisioner"
	"github.com/igloo-dev/igloo/internal/store"
)

// Worker polls the job queue and provisions workspaces.
type Worker struct {
	Store     store.Store
	ServerURL string
	inFlight  sync.Map // workspace_id → struct{}, prevents concurrent terraform for same workspace
}

// New creates a Worker. serverURL is injected into workspace containers so the
// agent knows where to dial home.
func New(st store.Store, serverURL string) *Worker {
	return &Worker{Store: st, ServerURL: serverURL}
}

// Enqueue creates a new pending job. Call this from API handlers.
func Enqueue(ctx context.Context, st store.Store, workspaceID string, action models.JobAction) error {
	job := &models.ProvisionJob{
		ID:          uuid.NewString(),
		WorkspaceID: workspaceID,
		Action:      action,
		Status:      models.JobPending,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		TTL:         time.Now().Add(7 * 24 * time.Hour).Unix(),
	}
	return st.CreateJob(ctx, job)
}

// Start launches concurrency worker goroutines. Each polls every 5 seconds.
// Blocks until ctx is cancelled.
func (w *Worker) Start(ctx context.Context, concurrency int) {
	w.recoverStaleJobs(ctx)
	for i := 0; i < concurrency; i++ {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					w.processNext(ctx)
				}
			}
		}()
	}
	log.Printf("[worker] started %d worker(s), polling every 5s", concurrency)
}

func (w *Worker) recoverStaleJobs(ctx context.Context) {
	stale, err := w.Store.ListStaleJobs(ctx, 15*time.Minute)
	if err != nil {
		log.Printf("[worker] stale job scan failed: %v", err)
		return
	}
	for _, job := range stale {
		if job.RetryCount >= models.JobMaxRetries {
			job.Status = models.JobFailed
			job.Error = "max retries exceeded (server restarted)"
		} else {
			job.RetryCount++
			job.Status = models.JobPending
			log.Printf("[worker] re-queuing stale job %s workspace=%s action=%s retry=%d",
				job.ID, job.WorkspaceID, job.Action, job.RetryCount)
		}
		job.UpdatedAt = time.Now()
		_ = w.Store.UpdateJob(ctx, job)
	}
	if len(stale) > 0 {
		log.Printf("[worker] recovered %d stale jobs on startup", len(stale))
	}
}

func (w *Worker) processNext(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[worker] panic recovered: %v", r)
		}
	}()

	job, err := w.Store.ClaimNextJob(ctx, "embedded")
	if err != nil {
		log.Printf("[worker] claim error: %v", err)
		return
	}
	if job == nil {
		return
	}

	// Per-workspace in-flight lock: prevents two workers from running terraform
	// against the same workspace simultaneously (S3 state lock would also catch
	// this, but we want to fail fast locally).
	if _, loaded := w.inFlight.LoadOrStore(job.WorkspaceID, struct{}{}); loaded {
		// Another goroutine is already working on this workspace; put job back to pending.
		job.Status = models.JobPending
		job.UpdatedAt = time.Now()
		_ = w.Store.UpdateJob(ctx, job)
		return
	}
	defer w.inFlight.Delete(job.WorkspaceID)

	log.Printf("[worker] processing job=%s action=%s workspace=%s retry=%d",
		job.ID, job.Action, job.WorkspaceID, job.RetryCount)

	ws, err := w.Store.GetWorkspace(ctx, job.WorkspaceID)
	if err != nil {
		job.Status = models.JobDone
		job.UpdatedAt = time.Now()
		_ = w.Store.UpdateJob(ctx, job)
		log.Printf("[worker] job %s skipped — workspace %s not found", job.ID, job.WorkspaceID)
		return
	}
	tmpl, _ := w.Store.GetTemplate(ctx, ws.TemplateID)

	// Skip start/stop if workspace is mid-delete
	if ws.Status == models.StatusDeleting && job.Action != models.JobDestroy {
		job.Status = models.JobDone
		job.UpdatedAt = time.Now()
		_ = w.Store.UpdateJob(ctx, job)
		log.Printf("[worker] job %s skipped — workspace %s is deleting", job.ID, job.WorkspaceID)
		return
	}

	var runErr error
	switch job.Action {
	case models.JobStart:
		sshKey := ""
		if user, e := w.Store.GetUser(ctx, ws.UserID); e == nil {
			sshKey = user.SSHPublicKey
		}
		runErr = w.launchFargate(ctx, ws, tmpl, w.ServerURL, sshKey)
		if runErr != nil {
			if cur, e := w.Store.GetWorkspace(ctx, ws.ID); e == nil {
				cur.Status = models.StatusError
				cur.UpdatedAt = time.Now()
				_ = w.Store.UpdateWorkspace(ctx, cur)
			}
		}

	case models.JobStop:
		runErr = w.sleepFargate(ctx, ws, tmpl)
		if runErr == nil {
			if cur, e := w.Store.GetWorkspace(ctx, ws.ID); e == nil {
				cur.Status = models.StatusSleeping
				cur.UpdatedAt = time.Now()
				_ = w.Store.UpdateWorkspace(ctx, cur)
			}
		}

	case models.JobDestroy:
		runErr = w.destroyFargate(ctx, ws, tmpl)
		if runErr == nil {
			_ = w.Store.DeleteWorkspace(ctx, ws.ID)
		}
	}

	w.finishJob(ctx, job, runErr)
}

func (w *Worker) finishJob(ctx context.Context, job *models.ProvisionJob, err error) {
	job.UpdatedAt = time.Now()
	if err == nil {
		job.Status = models.JobDone
		log.Printf("[worker] job %s done", job.ID)
	} else if job.RetryCount < models.JobMaxRetries {
		job.Status = models.JobPending
		job.RetryCount++
		job.Error = err.Error()
		log.Printf("[worker] job %s failed, retrying (%d/%d): %v",
			job.ID, job.RetryCount, models.JobMaxRetries, err)
	} else {
		job.Status = models.JobFailed
		job.Error = err.Error()
		log.Printf("[worker] job %s permanently failed: %v", job.ID, err)
	}
	_ = w.Store.UpdateJob(ctx, job)
}

// ─── Fargate provisioning ─────────────────────────────────────────────────────

func (w *Worker) launchFargate(ctx context.Context, ws *models.Workspace, tmpl *models.Template, serverURL, sshKey string) error {
	if tmpl == nil {
		return fmt.Errorf("template not found")
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

	logPath := provisionLogPath(ws.ID)
	if cur, e := w.Store.GetWorkspace(ctx, ws.ID); e == nil {
		cur.Meta["log_path"] = logPath
		_ = w.Store.UpdateWorkspace(ctx, cur)
	}

	vars := buildFargateVars(ws, serverURL, sshKey)
	vars["desired_count"] = "1"

	outputs, err := provisioner.Run(ctx, provisioner.Input{
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

	if cur, e := w.Store.GetWorkspace(ctx, ws.ID); e == nil {
		if svc := outputs["service_name"]; svc != "" {
			cur.Meta["service_name"] = svc
		}
		_ = w.Store.UpdateWorkspace(ctx, cur)
	}
	return nil
}

func (w *Worker) sleepFargate(ctx context.Context, ws *models.Workspace, tmpl *models.Template) error {
	if tmpl == nil {
		return fmt.Errorf("template not found")
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

	vars := buildFargateVars(ws, "", "")
	vars["desired_count"] = "0"

	_, err := provisioner.Run(ctx, provisioner.Input{
		WorkspaceID: ws.ID,
		Files:       files,
		Vars:        vars,
		StateBucket: bucket,
		StateRegion: region,
		LogPath:     provisionLogPath(ws.ID),
	}, provisioner.ActionApply)
	if err != nil {
		return fmt.Errorf("terraform apply: %w", err)
	}
	return nil
}

func (w *Worker) destroyFargate(ctx context.Context, ws *models.Workspace, tmpl *models.Template) error {
	if tmpl == nil {
		return fmt.Errorf("template not found")
	}
	files := tmpl.Files
	if len(files) == 0 {
		files = loadFilesFromDisk("aws-fargate")
	}
	if len(files) == 0 {
		return fmt.Errorf("no terraform files for aws-fargate template")
	}

	sshKey := ""
	if user, err := w.Store.GetUser(ctx, ws.UserID); err == nil {
		sshKey = user.SSHPublicKey
	}

	bucket := os.Getenv("IGLOO_TF_STATE_BUCKET")
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}

	vars := buildFargateVars(ws, "", sshKey)
	vars["desired_count"] = "0"

	_, err := provisioner.Run(ctx, provisioner.Input{
		WorkspaceID: ws.ID,
		Files:       files,
		Vars:        vars,
		StateBucket: bucket,
		StateRegion: region,
		LogPath:     provisionLogPath(ws.ID),
	}, provisioner.ActionDestroy)
	if err != nil {
		return fmt.Errorf("terraform destroy: %w", err)
	}
	return nil
}

func buildFargateVars(ws *models.Workspace, serverURL, sshKey string) map[string]string {
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

func provisionLogPath(workspaceID string) string {
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
