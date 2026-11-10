package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/igloo-dev/igloo/internal/guard"
	"github.com/igloo-dev/igloo/internal/models"
)

// Sandboxes are workspaces made for code: created from a program, driven through a small
// API (run a command, read or write a file), and removed automatically when their time is
// up. They are ordinary workspaces underneath, so a person can open the same one in a
// terminal or VS Code to see or take over what the code did.
//
// Safe by default: unless the caller says otherwise, a sandbox is created behind the egress
// gateway with a policy that allows nothing.

const (
	metaSandbox        = "sandbox"         // "true" on a sandbox
	metaSandboxExpires = "sandbox_expires" // RFC 3339 time the sandbox is deleted

	defaultSandboxTemplate = "tpl-kubernetes"
	defaultSandboxTTL      = time.Hour
	minSandboxTTL          = time.Minute
	maxSandboxTTL          = 24 * time.Hour

	maxExecArgs  = 256
	maxExecBytes = 128 << 10 // total size of the command line
	maxExecStdin = 1 << 20
	maxFileBytes = 8 << 20
)

// agentReplyGrace is how long past a command's own timeout the server waits for the agent's
// answer before giving up. A variable so tests can shorten it.
var agentReplyGrace = 10 * time.Second

// ─── messages exchanged with the agent (see cmd/agent/exec.go) ────────────────

type agentMsg struct {
	Type      string   `json:"type"`
	ID        string   `json:"id,omitempty"`
	Data      string   `json:"data,omitempty"`
	Path      string   `json:"path,omitempty"`
	Cmd       []string `json:"cmd,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	TimeoutS  int      `json:"timeout_s,omitempty"`
	Mode      uint32   `json:"mode,omitempty"`
	Exit      int      `json:"exit,omitempty"`
	Stdout    string   `json:"stdout,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	TimedOut  bool     `json:"timed_out,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Err       string   `json:"err,omitempty"`
}

var (
	errNotReady     = errors.New("the sandbox is not running yet")
	errAgentTimeout = errors.New("the sandbox did not answer in time")
)

// agentCall sends req to the workspace's agent and waits for the reply of type want.
func (h *Handler) agentCall(ctx context.Context, wsID string, req agentMsg, want string, wait time.Duration) (agentMsg, error) {
	if !h.Relay.AgentConnected(wsID) {
		return agentMsg{}, errNotReady
	}
	req.ID = uuid.NewString()
	ch, cancel := h.Relay.Subscribe(wsID) // before sending, so the reply cannot be missed
	defer cancel()
	raw, _ := json.Marshal(req)
	h.Relay.SendToAgent(wsID, raw)

	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case raw := <-ch:
			var m agentMsg
			if json.Unmarshal(raw, &m) == nil && m.ID == req.ID && m.Type == want {
				return m, nil
			}
		case <-timer.C:
			return agentMsg{}, errAgentTimeout
		case <-ctx.Done():
			return agentMsg{}, ctx.Err()
		}
	}
}

// sandboxFor loads the sandbox in the URL for its owner (or an admin). Anything else, including
// an ordinary workspace, is reported as not found.
func (h *Handler) sandboxFor(w http.ResponseWriter, r *http.Request) (*models.Workspace, bool) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || !h.canAccessWorkspace(r, ws) || ws.Meta[metaSandbox] != "true" {
		h.err(w, http.StatusNotFound, "sandbox not found")
		return nil, false
	}
	return ws, true
}

func (h *Handler) agentErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotReady):
		h.err(w, http.StatusConflict, err.Error())
	case errors.Is(err, errAgentTimeout):
		h.err(w, http.StatusGatewayTimeout, err.Error())
	default:
		h.err(w, http.StatusInternalServerError, "internal error")
	}
}

// POST /api/sandboxes
func (h *Handler) CreateSandbox(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name       string            `json:"name"`
		TemplateID string            `json:"template_id"`
		Image      string            `json:"image"`       // workspace image, if the template takes one
		TTLSeconds int               `json:"ttl_seconds"` // default 3600, 60..86400
		Guard      *guard.Policy     `json:"guard"`       // default: allow nothing
		Unguarded  bool              `json:"unguarded"`   // explicitly run without the egress gateway
		Vars       map[string]string `json:"vars"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "bad request")
		return
	}
	ttl := defaultSandboxTTL
	if body.TTLSeconds != 0 {
		ttl = time.Duration(body.TTLSeconds) * time.Second
	}
	if ttl < minSandboxTTL || ttl > maxSandboxTTL {
		h.err(w, http.StatusBadRequest, fmt.Sprintf("ttl_seconds must be between %d and %d", int(minSandboxTTL.Seconds()), int(maxSandboxTTL.Seconds())))
		return
	}
	if base, err := h.baseline(r.Context()); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	} else if max := base.MaxSandboxTTL(); max > 0 && ttl > max {
		h.err(w, http.StatusForbidden, fmt.Sprintf("your organisation limits sandboxes to %d seconds", int(max.Seconds())))
		return
	}
	if body.Unguarded && body.Guard != nil {
		h.err(w, http.StatusBadRequest, "give a guard policy or unguarded, not both")
		return
	}
	pol := body.Guard
	if pol == nil && !body.Unguarded {
		if !h.guardEnabled() {
			h.err(w, http.StatusConflict, "sandboxes run behind the egress gateway by default, and it is not configured on this server; enable it (guard.enabled) or pass unguarded=true to accept unrestricted network access")
			return
		}
		pol = &guard.Policy{} // allow nothing
	}

	id := uuid.NewString()
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "sandbox-" + id[:8]
	}
	tpl := body.TemplateID
	if tpl == "" {
		tpl = defaultSandboxTemplate
	}
	vars := map[string]string{}
	for k, v := range body.Vars {
		vars[k] = v
	}
	image := body.Image
	if image == "" {
		image = h.SandboxImage
	}
	if image != "" {
		vars["workspace_image"] = image
	}
	if vars["runtime_class"] == "" && h.SandboxRuntimeClass != "" {
		vars["runtime_class"] = h.SandboxRuntimeClass
	}
	expires := time.Now().Add(ttl).UTC()
	ws, code, msg := h.createWorkspace(r, createInput{
		Name: name, TemplateID: tpl, Vars: vars, Guard: pol,
		Meta: map[string]string{metaSandbox: "true", metaSandboxExpires: expires.Format(time.RFC3339)},
	})
	if code != 0 {
		h.err(w, code, msg)
		return
	}
	h.json(w, http.StatusCreated, map[string]any{"id": ws.ID, "name": ws.Name, "status": ws.Status, "expires_at": expires, "guarded": pol != nil})
}

// POST /api/sandboxes/{id}/exec {"cmd": ["sh","-c","echo hi"], "cwd": "", "timeout_s": 30, "stdin": ""}
func (h *Handler) SandboxExec(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.sandboxFor(w, r)
	if !ok {
		return
	}
	var body struct {
		Cmd      []string `json:"cmd"`
		Cwd      string   `json:"cwd"`
		TimeoutS int      `json:"timeout_s"`
		Stdin    string   `json:"stdin"`
	}
	if err := h.decode(r, &body); err != nil || len(body.Cmd) == 0 || body.Cmd[0] == "" {
		h.err(w, http.StatusBadRequest, "cmd must be a non-empty list, e.g. [\"sh\",\"-c\",\"echo hi\"]")
		return
	}
	size := 0
	for _, a := range body.Cmd {
		size += len(a)
	}
	if len(body.Cmd) > maxExecArgs || size > maxExecBytes || len(body.Stdin) > maxExecStdin {
		h.err(w, http.StatusRequestEntityTooLarge, "command or stdin too large")
		return
	}
	timeout := body.TimeoutS
	if timeout <= 0 {
		timeout = 60
	}
	if timeout > 600 {
		timeout = 600
	}
	m, err := h.agentCall(r.Context(), ws.ID, agentMsg{
		Type: "exec", Cmd: body.Cmd, Cwd: body.Cwd, TimeoutS: timeout,
		Data: base64.StdEncoding.EncodeToString([]byte(body.Stdin)),
	}, "exec_res", time.Duration(timeout)*time.Second+agentReplyGrace)
	if err != nil {
		h.agentErr(w, err)
		return
	}
	if m.Err != "" {
		h.err(w, http.StatusBadRequest, m.Err)
		return
	}
	stdout, _ := base64.StdEncoding.DecodeString(m.Stdout)
	stderr, _ := base64.StdEncoding.DecodeString(m.Stderr)
	h.json(w, http.StatusOK, map[string]any{
		"exit_code": m.Exit, "stdout": strings.ToValidUTF8(string(stdout), "�"), "stderr": strings.ToValidUTF8(string(stderr), "�"),
		"timed_out": m.TimedOut, "truncated": m.Truncated,
	})
}

// GET /api/sandboxes/{id}/files?path=...
func (h *Handler) SandboxReadFile(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.sandboxFor(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		h.err(w, http.StatusBadRequest, "path is required")
		return
	}
	m, err := h.agentCall(r.Context(), ws.ID, agentMsg{Type: "fs_read", Path: path}, "fs_res", 30*time.Second+agentReplyGrace)
	if err != nil {
		h.agentErr(w, err)
		return
	}
	if m.Err != "" {
		h.err(w, http.StatusBadRequest, m.Err)
		return
	}
	data, _ := base64.StdEncoding.DecodeString(m.Data)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

// PUT /api/sandboxes/{id}/files?path=...&mode=0644 (raw body)
func (h *Handler) SandboxWriteFile(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.sandboxFor(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		h.err(w, http.StatusBadRequest, "path is required")
		return
	}
	var mode uint64
	if ms := r.URL.Query().Get("mode"); ms != "" {
		var err error
		if mode, err = strconv.ParseUint(ms, 8, 32); err != nil || mode > 0o777 {
			h.err(w, http.StatusBadRequest, "mode must be an octal permission such as 0644")
			return
		}
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFileBytes))
	if err != nil {
		h.err(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("files are limited to %d bytes", maxFileBytes))
		return
	}
	m, err := h.agentCall(r.Context(), ws.ID, agentMsg{
		Type: "fs_write", Path: path, Mode: uint32(mode), Data: base64.StdEncoding.EncodeToString(data),
	}, "fs_res", 30*time.Second+agentReplyGrace)
	if err != nil {
		h.agentErr(w, err)
		return
	}
	if m.Err != "" {
		h.err(w, http.StatusBadRequest, m.Err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── expiry ───────────────────────────────────────────────────────────────────

// expireSandboxes starts deleting every sandbox whose time is up and reports how many.
func (h *Handler) expireSandboxes(ctx context.Context) int {
	all, err := h.Store.ListWorkspaces(ctx, "", "")
	if err != nil {
		log.Printf("[sandbox] expiry scan failed: %v", err)
		return 0
	}
	base, err := h.baseline(ctx)
	if err != nil {
		log.Printf("[sandbox] cannot read the network baseline, using each sandbox's own expiry: %v", err)
	}
	n := 0
	for _, ws := range all {
		if ws.Meta[metaSandbox] != "true" || ws.Status == models.StatusDeleting {
			continue
		}
		exp, err := time.Parse(time.RFC3339, ws.Meta[metaSandboxExpires])
		if err != nil {
			continue // no usable expiry
		}
		if max := base.MaxSandboxTTL(); max > 0 && ws.CreatedAt.Add(max).Before(exp) {
			exp = ws.CreatedAt.Add(max) // the cap was tightened after this sandbox was made
		}
		if time.Now().Before(exp) {
			continue // not due yet
		}
		log.Printf("[sandbox] %s expired, deleting", ws.ID)
		h.beginDelete(ctx, ws)
		n++
	}
	return n
}

// StartSandboxJanitor deletes expired sandboxes every interval until ctx is cancelled.
func (h *Handler) StartSandboxJanitor(ctx context.Context, every time.Duration) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.expireSandboxes(ctx)
			}
		}
	}()
}
