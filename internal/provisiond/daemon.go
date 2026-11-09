// Package provisiond implements the provisioner daemon that connects to the igloo
// server via WebSocket and executes Terraform to provision/deprovision workspaces.
//
// Protocol (demand-driven, zero polling):
//
//	daemon → server: {"type":"acquire"}          — "I have a free worker slot"
//	server → daemon: {"type":"job", "job":...}   — here's work (instant, no delay)
//	daemon → server: {"type":"ping","job_id":"..."} — heartbeat while running
//	daemon → server: {"type":"done","job_id":"...","outputs":{...}}
//	daemon → server: {"type":"fail","job_id":"...","error":"..."}
package provisiond

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/provisioner"
)

// runner executes Terraform; provisioner.Run in production, a fake in tests.
type runner func(ctx context.Context, in provisioner.Input, action provisioner.Action) (map[string]string, error)

// Daemon connects to the igloo server over a single persistent WebSocket and
// runs up to concurrency Terraform jobs in parallel.
//
// Jobs belong to the daemon, not to a connection: a dropped WebSocket (network
// blip, server rollout) must not kill a running `terraform apply`. Results and
// slot requests go to whichever connection is current, and results produced
// while disconnected are held until the next one.
type Daemon struct {
	ServerURL string
	Token     string
	ID        string
	inFlight  sync.Map // workspace_id → struct{}

	run         runner
	initBackoff time.Duration
	deferDelay  time.Duration // pause before asking for work again after deferring a job

	mu       sync.Mutex
	cur      *session // nil while disconnected
	running  int      // jobs executing right now, across connections
	pending  []wsMsg  // done/fail results waiting for a connection
	draining bool     // shutting down: take no new work, let running jobs finish
}

// session is one live WebSocket connection.
type session struct {
	send chan wsMsg
	ctx  context.Context
	kick func() // closes the underlying connection
}

func New(serverURL, token string) *Daemon {
	return &Daemon{
		ServerURL:   serverURL,
		Token:       token,
		ID:          uuid.NewString(),
		run:         provisioner.Run,
		initBackoff: time.Second,
		deferDelay:  2 * time.Second,
	}
}

// emit hands msg to the current connection without blocking. A reliable message
// (a job result) that can't be delivered now is kept for the next connection.
func (d *Daemon) emit(msg wsMsg, reliable bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cur != nil && d.cur.ctx.Err() == nil {
		select {
		case d.cur.send <- msg:
			return
		default:
		}
	}
	if reliable {
		d.pending = append(d.pending, msg)
	}
}

// wsMsg mirrors the server's wsMsg envelope.
type wsMsg struct {
	Type        string            `json:"type"`
	JobID       string            `json:"job_id,omitempty"`
	WorkspaceID string            `json:"workspace_id,omitempty"`
	Line        string            `json:"line,omitempty"`
	Outputs     map[string]string `json:"outputs,omitempty"`
	Error       string            `json:"error,omitempty"`
	Job         *claimResponse    `json:"job,omitempty"`
}

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

// Drain prepares for shutdown: it stops asking for work, lets running jobs finish
// and delivers their results, waiting up to timeout. It reports whether everything
// finished and was delivered; the caller then cancels the Start context, which
// stops anything still running.
//
// The server remembers every free slot the daemon announced and keeps assigning
// jobs to them, so draining also drops the connection: the daemon reconnects
// without announcing any slots, and the server forgets the old ones.
func (d *Daemon) Drain(timeout time.Duration) bool {
	d.mu.Lock()
	d.draining = true
	cur, running := d.cur, d.running
	d.mu.Unlock()
	if running == 0 {
		return true
	}
	log.Printf("[provisiond] draining: waiting up to %v for %d running job(s)", timeout, running)
	if cur != nil {
		cur.kick()
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		idle := d.running == 0 && len(d.pending) == 0 && (d.cur == nil || len(d.cur.send) == 0)
		d.mu.Unlock()
		if idle {
			time.Sleep(200 * time.Millisecond) // let the writer put the last message on the wire
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// Start connects to the server and maintains the connection until ctx is cancelled.
func (d *Daemon) Start(ctx context.Context, concurrency int) {
	log.Printf("[provisiond] daemon %s starting (%d slots, server=%s)", d.ID, concurrency, d.ServerURL)
	go d.connectLoop(ctx, concurrency)
}

func (d *Daemon) connectLoop(ctx context.Context, concurrency int) {
	backoff := d.initBackoff
	for {
		started := time.Now()
		err := d.runConnection(ctx, concurrency)
		if ctx.Err() != nil {
			return
		}
		// A connection that stayed up for a while was healthy: don't carry old failures forward.
		if time.Since(started) > 30*time.Second {
			backoff = d.initBackoff
		}
		log.Printf("[provisiond] disconnected (%v), reconnecting in %v", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (d *Daemon) runConnection(ctx context.Context, concurrency int) error {
	u, _ := url.Parse(d.ServerURL)
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/api/provisioner/ws"
	q := u.Query()
	q.Set("id", d.ID)
	u.RawQuery = q.Encode()

	// The provisioner key is the most powerful credential in the system (it can
	// claim any tenant's job): keep it out of the URL, where proxies and access
	// logs record it.
	hdr := http.Header{"Authorization": {"Bearer " + d.Token}}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), hdr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	log.Printf("[provisiond] connected to %s", d.ServerURL)

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &session{send: make(chan wsMsg, 256), ctx: connCtx, kick: func() { conn.Close() }}

	// Make this the current connection: replay results held while disconnected and
	// ask only for the slots not already occupied by jobs that survived the drop.
	d.mu.Lock()
	d.cur = s
	for _, m := range d.pending {
		s.send <- m
	}
	d.pending = nil
	for i := d.running; i < concurrency && !d.draining; i++ {
		s.send <- wsMsg{Type: "acquire"}
	}
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		if d.cur == s {
			d.cur = nil
		}
		d.mu.Unlock()
	}()

	// Single writer for the connection.
	go func() {
		for {
			select {
			case msg := <-s.send:
				if err := conn.WriteJSON(msg); err != nil {
					cancel()
					return
				}
			case <-connCtx.Done():
				return
			}
		}
	}()

	// Read loop
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		var msg wsMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "job":
			if msg.Job == nil {
				continue
			}
			d.mu.Lock()
			d.running++
			d.mu.Unlock()
			// Terraform runs on the daemon's context, not this connection's.
			go d.runJob(ctx, msg.Job)
		case "ping":
			// server keepalive — no action needed
		}
	}
}

// runJob executes one job and reports on whichever connection is current when it finishes.
func (d *Daemon) runJob(ctx context.Context, job *claimResponse) {
	defer func() {
		// Worker slot free — ask for the next job on the current connection (if
		// we're disconnected the next connect computes the free slots itself).
		d.mu.Lock()
		d.running--
		draining := d.draining
		d.mu.Unlock()
		if !draining {
			d.emit(wsMsg{Type: "acquire"}, false)
		}
	}()

	// Per-workspace lock prevents two slots running terraform on the same workspace simultaneously.
	if _, loaded := d.inFlight.LoadOrStore(job.WorkspaceID, struct{}{}); loaded {
		// Not a failure: hand the job back to the queue (no retry used) and wait a
		// moment before taking work again, or the same job returns instantly.
		d.emit(wsMsg{Type: "defer", JobID: job.JobID}, true)
		time.Sleep(d.deferDelay)
		return
	}
	defer d.inFlight.Delete(job.WorkspaceID)

	log.Printf("[provisiond] job=%s action=%s workspace=%s retry=%d",
		job.JobID, job.Action, job.WorkspaceID, job.RetryCount)

	// Heartbeat goroutine
	hbCtx, hbCancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(90 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				d.emit(wsMsg{Type: "ping", JobID: job.JobID}, false)
			case <-hbCtx.Done():
				return
			}
		}
	}()

	logFunc := func(line string) {
		d.emit(wsMsg{Type: "log", JobID: job.JobID, WorkspaceID: job.WorkspaceID, Line: line}, false)
	}

	outputs, err := d.terraform(ctx, job, logFunc)
	hbCancel()

	if err != nil {
		log.Printf("[provisiond] job=%s failed: %v", job.JobID, err)
		d.emit(wsMsg{Type: "fail", JobID: job.JobID, WorkspaceID: job.WorkspaceID, Error: err.Error()}, true)
		return
	}
	log.Printf("[provisiond] job=%s done", job.JobID)
	d.emit(wsMsg{Type: "done", JobID: job.JobID, WorkspaceID: job.WorkspaceID, Outputs: outputs}, true)
}

func (d *Daemon) terraform(ctx context.Context, job *claimResponse, logFunc func(string)) (map[string]string, error) {
	if len(job.Files) == 0 {
		return nil, fmt.Errorf("no terraform files")
	}
	if job.StateBucket == "" {
		return nil, fmt.Errorf("state_bucket not set")
	}

	var action provisioner.Action
	desiredCount := "1"
	switch job.Action {
	case "start":
		action = provisioner.ActionApply
	case "stop":
		action = provisioner.ActionApply
		desiredCount = "0"
	case "destroy":
		action = provisioner.ActionDestroy
		desiredCount = "0"
	default:
		return nil, fmt.Errorf("unknown action: %s", job.Action)
	}

	return d.run(ctx, provisioner.Input{
		WorkspaceID: job.WorkspaceID,
		Files:       job.Files,
		Vars:        d.buildVars(job, desiredCount),
		StateBucket: job.StateBucket,
		StateRegion: job.StateRegion,
		LogPath:     logFilePath(job.WorkspaceID),
		LogFunc:     logFunc,
	}, action)
}

func (d *Daemon) buildVars(job *claimResponse, desiredCount string) map[string]string {
	vars := make(map[string]string)
	for k, v := range job.Vars {
		vars[k] = v
	}
	vars["workspace_id"] = job.WorkspaceID
	vars["workspace_name"] = job.WorkspaceName
	vars["server_url"] = job.ServerURL
	vars["agent_token"] = job.AgentToken
	vars["desired_count"] = desiredCount
	vars["cluster_name"] = os.Getenv("IGLOO_ECS_CLUSTER")
	vars["task_execution_role_arn"] = os.Getenv("IGLOO_ECS_EXECUTION_ROLE_ARN")
	vars["task_role_arn"] = os.Getenv("IGLOO_ECS_TASK_ROLE_ARN")
	vars["subnet_ids"] = os.Getenv("IGLOO_ECS_SUBNETS")
	vars["security_group_id"] = os.Getenv("IGLOO_ECS_SG")
	if vars["workspace_image"] == "" {
		vars["workspace_image"] = os.Getenv("IGLOO_WORKSPACE_IMAGE")
	}
	if vars["region"] == "" {
		if r := os.Getenv("AWS_REGION"); r != "" {
			vars["region"] = r
		} else {
			vars["region"] = "us-east-1"
		}
	}
	return vars
}

func logFilePath(workspaceID string) string {
	dir := os.TempDir() + "/igloo-logs"
	_ = os.MkdirAll(dir, 0755)
	return dir + "/" + workspaceID + ".log"
}
