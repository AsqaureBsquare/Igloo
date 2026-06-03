// Package provisiond implements the provisioner daemon that connects to the igloo
// server via WebSocket and executes Terraform to provision/deprovision workspaces.
//
// Protocol (demand-driven, zero polling):
//   daemon → server: {"type":"acquire"}          — "I have a free worker slot"
//   server → daemon: {"type":"job", "job":...}   — here's work (instant, no delay)
//   daemon → server: {"type":"ping","job_id":"..."} — heartbeat while running
//   daemon → server: {"type":"done","job_id":"...","outputs":{...}}
//   daemon → server: {"type":"fail","job_id":"...","error":"..."}
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

// Daemon connects to the igloo server over a single persistent WebSocket and
// runs up to concurrency Terraform jobs in parallel.
type Daemon struct {
	ServerURL string
	Token     string
	ID        string
	inFlight  sync.Map // workspace_id → struct{}
}

func New(serverURL, token string) *Daemon {
	return &Daemon{
		ServerURL: serverURL,
		Token:     token,
		ID:        uuid.NewString(),
	}
}

// wsMsg mirrors the server's wsMsg envelope.
type wsMsg struct {
	Type    string            `json:"type"`
	JobID   string            `json:"job_id,omitempty"`
	Outputs map[string]string `json:"outputs,omitempty"`
	Error   string            `json:"error,omitempty"`
	Job     *claimResponse    `json:"job,omitempty"`
}

type claimResponse struct {
	JobID         string            `json:"job_id"`
	Action        string            `json:"action"`
	RetryCount    int               `json:"retry_count"`
	WorkspaceID   string            `json:"workspace_id"`
	WorkspaceName string            `json:"workspace_name"`
	AgentToken    string            `json:"agent_token"`
	Vars          map[string]string `json:"vars"`
	Meta          map[string]string `json:"meta"`
	Files         map[string]string `json:"files"`
	SSHKey        string            `json:"ssh_key"`
	ServerURL     string            `json:"server_url"`
	StateBucket   string            `json:"state_bucket"`
	StateRegion   string            `json:"state_region"`
}

// Start connects to the server and maintains the connection until ctx is cancelled.
func (d *Daemon) Start(ctx context.Context, concurrency int) {
	log.Printf("[provisiond] daemon %s starting (%d slots, server=%s)", d.ID, concurrency, d.ServerURL)
	go d.connectLoop(ctx, concurrency)
}

func (d *Daemon) connectLoop(ctx context.Context, concurrency int) {
	backoff := time.Second
	for {
		err := d.runConnection(ctx, concurrency)
		if ctx.Err() != nil {
			return
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
	q.Set("token", d.Token)
	q.Set("id", d.ID)
	u.RawQuery = q.Encode()

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), http.Header{})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	log.Printf("[provisiond] connected to %s", d.ServerURL)

	// sendCh is the single writer for the connection.
	sendCh := make(chan wsMsg, concurrency*2)
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Write loop
	go func() {
		for {
			select {
			case msg, ok := <-sendCh:
				if !ok {
					return
				}
				if err := conn.WriteJSON(msg); err != nil {
					cancel()
					return
				}
			case <-connCtx.Done():
				return
			}
		}
	}()

	// Signal all available worker slots — server will push jobs immediately.
	for i := 0; i < concurrency; i++ {
		sendCh <- wsMsg{Type: "acquire"}
	}

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
			job := msg.Job
			go func() {
				d.runJob(connCtx, job, sendCh)
				// Worker slot free — ask for the next job
				select {
				case sendCh <- wsMsg{Type: "acquire"}:
				case <-connCtx.Done():
				}
			}()
		case "ping":
			// server keepalive — no action needed
		}
	}
}

func (d *Daemon) runJob(ctx context.Context, job *claimResponse, sendCh chan<- wsMsg) {
	// Per-workspace lock prevents two slots running terraform on the same workspace simultaneously.
	if _, loaded := d.inFlight.LoadOrStore(job.WorkspaceID, struct{}{}); loaded {
		sendCh <- wsMsg{Type: "fail", JobID: job.JobID, Error: "workspace already being provisioned"}
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
				select {
				case sendCh <- wsMsg{Type: "ping", JobID: job.JobID}:
				default:
				}
			case <-hbCtx.Done():
				return
			}
		}
	}()

	outputs, err := d.terraform(ctx, job)
	hbCancel()

	if err != nil {
		log.Printf("[provisiond] job=%s failed: %v", job.JobID, err)
		select {
		case sendCh <- wsMsg{Type: "fail", JobID: job.JobID, Error: err.Error()}:
		case <-ctx.Done():
		}
		return
	}

	select {
	case sendCh <- wsMsg{Type: "done", JobID: job.JobID, Outputs: outputs}:
		log.Printf("[provisiond] job=%s done", job.JobID)
	case <-ctx.Done():
	}
}

func (d *Daemon) terraform(ctx context.Context, job *claimResponse) (map[string]string, error) {
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

	return provisioner.Run(ctx, provisioner.Input{
		WorkspaceID: job.WorkspaceID,
		Files:       job.Files,
		Vars:        d.buildVars(job, desiredCount),
		StateBucket: job.StateBucket,
		StateRegion: job.StateRegion,
		LogPath:     logFilePath(job.WorkspaceID),
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
	vars["ssh_public_key"] = job.SSHKey
	vars["desired_count"] = desiredCount
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

func logFilePath(workspaceID string) string {
	dir := os.TempDir() + "/igloo-logs"
	_ = os.MkdirAll(dir, 0755)
	return dir + "/" + workspaceID + ".log"
}
