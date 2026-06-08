package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/models"
)

var provisionerUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsMsg is the envelope for all provisioner ↔ server WebSocket messages.
type wsMsg struct {
	Type        string            `json:"type"`                  // acquire | ping | log | done | fail | job
	JobID       string            `json:"job_id,omitempty"`
	WorkspaceID string            `json:"workspace_id,omitempty"` // log | done | fail
	Line        string            `json:"line,omitempty"`         // log
	Outputs     map[string]string `json:"outputs,omitempty"`      // done
	Error       string            `json:"error,omitempty"`        // fail
	Job         *claimResponse    `json:"job,omitempty"`          // server→daemon: type=job
}

// ProvisionerWS handles GET /api/provisioner/ws.
// Each daemon connects once; concurrency is expressed by sending multiple "acquire" messages.
func (h *Handler) ProvisionerWS(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		h.err(w, http.StatusUnauthorized, "token required")
		return
	}
	key, err := h.Store.GetProvisionerKeyByTokenHash(r.Context(), hashToken(token))
	if err != nil {
		h.err(w, http.StatusUnauthorized, "invalid provisioner key")
		return
	}
	_ = h.Store.TouchProvisionerKey(r.Context(), key.ID)

	provisionerID := r.URL.Query().Get("id")
	if provisionerID == "" {
		h.err(w, http.StatusBadRequest, "id required")
		return
	}

	conn, err := provisionerUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[provisioner-ws] upgrade error: %v", err)
		return
	}
	defer conn.Close()

	log.Printf("[provisioner-ws] provisioner %s connected", provisionerID)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// send is the only goroutine allowed to write to conn.
	sendCh := make(chan wsMsg, 32)
	go func() {
		for {
			select {
			case msg, ok := <-sendCh:
				if !ok {
					return
				}
				if err := conn.WriteJSON(msg); err != nil {
					log.Printf("[provisioner-ws] write error: %v", err)
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Server-side keepalive
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				select {
				case sendCh <- wsMsg{Type: "ping"}:
				default:
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Read loop — each message from the daemon is processed here.
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var msg wsMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		switch msg.Type {

		case "acquire":
			// Daemon has a free worker slot. Park it in the registry so TryDispatch
			// can push a job to it — either immediately (if jobs are pending) or later
			// (when the next workspace action is triggered).
			jobCh, deregister := h.Registry.Park(provisionerID, ctx.Done())

			// Always start the goroutine FIRST so jobCh has a reader before we
			// potentially write to it via TryDispatch.
			go func() {
				defer deregister()
				select {
				case cr := <-jobCh:
					select {
					case sendCh <- wsMsg{Type: "job", Job: cr}:
					case <-ctx.Done():
					}
				case <-ctx.Done():
				}
			}()

			// Attempt to dispatch any already-pending DB job to this slot.
			h.Registry.TryDispatch(r.Context(), h)

		case "ping":
			if msg.JobID != "" {
				_ = h.Store.UpdateJobHeartbeat(r.Context(), msg.JobID)
			}

		case "log":
			if msg.WorkspaceID != "" && msg.Line != "" {
				h.LogBuf.Append(msg.WorkspaceID, msg.Line)
			}

		case "done":
			if msg.WorkspaceID != "" {
				h.LogBuf.Append(msg.WorkspaceID, "✓ Provisioning complete")
			}
			h.handleJobDone(r.Context(), msg.JobID, msg.Outputs)

		case "fail":
			if msg.WorkspaceID != "" {
				h.LogBuf.Append(msg.WorkspaceID, "✗ Failed: "+msg.Error)
			}
			h.handleJobFail(r.Context(), msg.JobID, msg.Error)
		}
	}

	cancel()
	log.Printf("[provisioner-ws] provisioner %s disconnected", provisionerID)
}

// TryImmediateDispatch is called after Enqueue to push the job to a waiting daemon.
// Falls through silently if no daemon is connected — the job stays in DynamoDB.
func (h *Handler) TryImmediateDispatch(ctx context.Context) {
	if h.Registry != nil {
		h.Registry.TryDispatch(ctx, h)
	}
}

// ProvisionerAuth is middleware that validates a provisioner key token.
func (h *Handler) ProvisionerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		token, _ := strings.CutPrefix(authHeader, "Bearer ")
		if token == "" {
			h.err(w, http.StatusUnauthorized, "token required")
			return
		}
		if _, err := h.Store.GetProvisionerKeyByTokenHash(r.Context(), hashToken(token)); err != nil {
			h.err(w, http.StatusUnauthorized, "invalid provisioner key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// claimResponse is what the daemon receives after claiming a job.
type claimResponse struct {
	JobID         string            `json:"job_id"`
	Action        string            `json:"action"`
	RetryCount    int               `json:"retry_count"`
	Provider      string            `json:"provider"`
	WorkspaceID   string            `json:"workspace_id"`
	WorkspaceName string            `json:"workspace_name"`
	AgentToken    string            `json:"agent_token"`
	Vars          map[string]string `json:"vars"`
	Meta          map[string]string `json:"meta"`
	Files         map[string]string `json:"files"`
	ServerURL     string            `json:"server_url"`
	StateBucket   string            `json:"state_bucket"`
	StateRegion   string            `json:"state_region"`
}

// ClaimJob handles POST /api/provisioner/claim.
func (h *Handler) ClaimJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProvisionerID string `json:"provisioner_id"`
	}
	if err := h.decode(r, &req); err != nil || req.ProvisionerID == "" {
		h.err(w, http.StatusBadRequest, "provisioner_id required")
		return
	}

	job, err := h.Store.ClaimNextJob(r.Context(), req.ProvisionerID)
	if err != nil {
		log.Printf("[provisioner-api] claim error: %v", err)
		h.err(w, http.StatusInternalServerError, "claim failed")
		return
	}
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ws, err := h.Store.GetWorkspace(r.Context(), job.WorkspaceID)
	if err != nil {
		job.Status = models.JobDone
		job.UpdatedAt = time.Now()
		_ = h.Store.UpdateJob(r.Context(), job)
		log.Printf("[provisioner-api] job %s: workspace %s not found, skipping", job.ID, job.WorkspaceID)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var files map[string]string
	if tmpl, err := h.Store.GetTemplate(r.Context(), ws.TemplateID); err == nil && tmpl != nil {
		files = tmpl.Files
	}

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}

	h.json(w, http.StatusOK, claimResponse{
		JobID:         job.ID,
		Action:        string(job.Action),
		RetryCount:    job.RetryCount,
		WorkspaceID:   ws.ID,
		WorkspaceName: ws.Name,
		AgentToken:    ws.AgentToken,
		Vars:          ws.Vars,
		Meta:          ws.Meta,
		Files:         files,
		ServerURL:     os.Getenv("IGLOO_SERVER_URL"),
		StateBucket:   os.Getenv("IGLOO_TF_STATE_BUCKET"),
		StateRegion:   region,
	})
}

// JobPing handles POST /api/provisioner/jobs/{id}/ping.
func (h *Handler) JobPing(w http.ResponseWriter, r *http.Request) {
	if err := h.Store.UpdateJobHeartbeat(r.Context(), chi.URLParam(r, "id")); err != nil {
		h.err(w, http.StatusInternalServerError, "heartbeat failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// JobDone handles POST /api/provisioner/jobs/{id}/done.
func (h *Handler) JobDone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Outputs map[string]string `json:"outputs"`
	}
	_ = h.decode(r, &req)
	h.handleJobDone(r.Context(), chi.URLParam(r, "id"), req.Outputs)
	w.WriteHeader(http.StatusNoContent)
}

// JobFail handles POST /api/provisioner/jobs/{id}/fail.
func (h *Handler) JobFail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Error string `json:"error"`
	}
	_ = h.decode(r, &req)
	h.handleJobFail(r.Context(), chi.URLParam(r, "id"), req.Error)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleJobDone(ctx context.Context, jobID string, outputs map[string]string) {
	job, err := h.Store.GetJob(ctx, jobID)
	if err != nil {
		log.Printf("[provisioner-api] done: job %s not found", jobID)
		return
	}
	job.Status = models.JobDone
	job.UpdatedAt = time.Now()
	_ = h.Store.UpdateJob(ctx, job)

	if len(outputs) > 0 {
		if ws, err := h.Store.GetWorkspace(ctx, job.WorkspaceID); err == nil {
			if ws.Meta == nil {
				ws.Meta = make(map[string]string)
			}
			for k, v := range outputs {
				ws.Meta[k] = v
			}
			_ = h.Store.UpdateWorkspace(ctx, ws)
		}
	}
	if job.Action == models.JobDestroy {
		if err := h.Store.DeleteWorkspace(ctx, job.WorkspaceID); err != nil {
			log.Printf("[provisioner-api] job %s: failed to delete workspace %s: %v", jobID, job.WorkspaceID, err)
		}
	}
	log.Printf("[provisioner-api] job %s done (workspace=%s action=%s)", jobID, job.WorkspaceID, job.Action)
}

func (h *Handler) handleJobFail(ctx context.Context, jobID, errMsg string) {
	job, err := h.Store.GetJob(ctx, jobID)
	if err != nil {
		log.Printf("[provisioner-api] fail: job %s not found", jobID)
		return
	}
	job.Error = errMsg
	job.UpdatedAt = time.Now()
	if job.RetryCount < models.JobMaxRetries {
		job.RetryCount++
		job.Status = models.JobPending
		log.Printf("[provisioner-api] job %s failed, retry %d/%d: %s", jobID, job.RetryCount, models.JobMaxRetries, errMsg)
		// Re-dispatch immediately if a provisioner is waiting
		h.TryImmediateDispatch(ctx)
	} else {
		job.Status = models.JobFailed
		log.Printf("[provisioner-api] job %s permanently failed: %s", jobID, errMsg)
		if job.Action == models.JobDestroy {
			if err := h.Store.DeleteWorkspace(ctx, job.WorkspaceID); err != nil {
				log.Printf("[provisioner-api] job %s: force-delete workspace %s failed: %v", jobID, job.WorkspaceID, err)
			} else {
				log.Printf("[provisioner-api] job %s: force-deleted workspace %s after destroy failure", jobID, job.WorkspaceID)
			}
		} else {
			if ws, err := h.Store.GetWorkspace(ctx, job.WorkspaceID); err == nil {
				ws.Status = models.StatusError
				ws.UpdatedAt = time.Now()
				_ = h.Store.UpdateWorkspace(ctx, ws)
			}
		}
	}
	_ = h.Store.UpdateJob(ctx, job)
}
