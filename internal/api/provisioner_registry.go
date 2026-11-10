package api

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

	"github.com/igloo-dev/igloo/internal/models"
)

// ProvisionerRegistry parks "acquire" tokens from connected provisioner daemons.
// When a job is enqueued, TryDispatch immediately sends it to a waiting slot —
// no polling, sub-millisecond dispatch latency.
type ProvisionerRegistry struct {
	mu      sync.Mutex
	waiters []registryWaiter
}

type registryWaiter struct {
	provisionerID string
	ch            chan<- *claimResponse // buffered(1)
	dead          <-chan struct{}       // closed when the WS connection drops
}

func NewProvisionerRegistry() *ProvisionerRegistry {
	return &ProvisionerRegistry{}
}

// Park registers one available worker slot from a provisioner daemon.
// Returns a channel the WS handler blocks on waiting for a job, plus a deregister func.
func (r *ProvisionerRegistry) Park(provisionerID string, dead <-chan struct{}) (<-chan *claimResponse, func()) {
	ch := make(chan *claimResponse, 1)
	r.mu.Lock()
	r.waiters = append(r.waiters, registryWaiter{provisionerID, ch, dead})
	r.mu.Unlock()

	deregister := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i, w := range r.waiters {
			if w.ch == ch {
				r.waiters = append(r.waiters[:i], r.waiters[i+1:]...)
				return
			}
		}
	}
	return ch, deregister
}

// TryDispatch claims the next pending job from the store and pushes it to a
// waiting provisioner. Returns true if a job was dispatched. Safe to call
// concurrently (e.g. from multiple HTTP handlers at once).
func (r *ProvisionerRegistry) TryDispatch(ctx context.Context, h *Handler) bool {
	for {
		r.mu.Lock()
		if len(r.waiters) == 0 {
			r.mu.Unlock()
			return false
		}
		w := r.waiters[0]
		r.waiters = r.waiters[1:]
		r.mu.Unlock()

		// Skip dead connections — their goroutines have already called deregister.
		select {
		case <-w.dead:
			continue
		default:
		}

		job, err := h.Store.ClaimNextJob(ctx, w.provisionerID)
		if err != nil {
			log.Printf("[registry] claim error: %v", err)
			// Put waiter back so it can try again later.
			r.requeue(w)
			return false
		}
		if job == nil {
			// No pending jobs — put waiter back; it will receive a job when one arrives.
			r.requeue(w)
			return false
		}

		cr, err := buildClaimResponse(ctx, h, job)
		if err != nil {
			// Workspace deleted — mark job done, put waiter back, loop to try next job.
			job.Status = models.JobDone
			_ = h.Store.UpdateJob(ctx, job)
			r.requeue(w)
			continue
		}

		w.ch <- cr // buffered(1), never blocks
		log.Printf("[registry] dispatched job=%s action=%s → provisioner=%s",
			cr.JobID, cr.Action, w.provisionerID)
		return true
	}
}

// HasWaiters returns true if there is at least one live parked provisioner slot.
func (r *ProvisionerRegistry) HasWaiters() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.waiters) > 0
}

// StartDispatchLoop runs a background goroutine that retries TryDispatch every 15 seconds.
// This is the safety net for the race where a pending job exists in the DB when a
// provisioner connects but ClaimNextJob transiently returns nil on the first attempt.
func (r *ProvisionerRegistry) StartDispatchLoop(ctx context.Context, h *Handler) {
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if r.HasWaiters() {
					if dispatched := r.TryDispatch(ctx, h); dispatched {
						log.Printf("[registry] dispatch-loop picked up orphaned pending job")
					}
				}
			}
		}
	}()
}

func (r *ProvisionerRegistry) requeue(w registryWaiter) {
	r.mu.Lock()
	r.waiters = append([]registryWaiter{w}, r.waiters...)
	r.mu.Unlock()
}

// buildClaimResponse fetches workspace + template + user and assembles the job context payload.
func buildClaimResponse(ctx context.Context, h *Handler, job *models.ProvisionJob) (*claimResponse, error) {
	ws, err := h.Store.GetWorkspace(ctx, job.WorkspaceID)
	if err != nil {
		return nil, err
	}

	var files map[string]string
	if tmpl, err := h.Store.GetTemplate(ctx, ws.TemplateID); err == nil && tmpl != nil {
		files = tmpl.Files
	}

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}

	return &claimResponse{
		JobID:         job.ID,
		Action:        string(job.Action),
		RetryCount:    job.RetryCount,
		Provider:      ws.Provider,
		WorkspaceID:   ws.ID,
		WorkspaceName: ws.Name,
		AgentToken:    ws.AgentToken,
		Vars:          h.claimVars(ws),
		Meta:          ws.Meta,
		Files:         files,
		ServerURL:     os.Getenv("IGLOO_SERVER_URL"),
		StateBucket:   os.Getenv("IGLOO_TF_STATE_BUCKET"),
		StateRegion:   region,
	}, nil
}
