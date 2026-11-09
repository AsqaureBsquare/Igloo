package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Programmatic access to a workspace (used by the sandbox API): run a command, and read or
// write files. These run as their own processes, not in the shared terminal, so callers
// don't interfere with each other or with a person using the terminal.

const (
	maxExecOutput   = 1 << 20 // per stream; more is cut off and reported
	maxExecTimeout  = 10 * time.Minute
	defExecTimeout  = 60 * time.Second
	maxFileTransfer = 8 << 20
)

type execRequest struct {
	Argv    []string
	Cwd     string
	Stdin   []byte
	Timeout time.Duration
}

type execResult struct {
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	TimedOut  bool
	Truncated bool
	Err       string // the command could not be started
}

// capWriter keeps the first max bytes and notes whether anything was dropped.
type capWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil // never fail the command because the caller's buffer is full
}

func runExec(req execRequest, defaultDir string) execResult {
	if len(req.Argv) == 0 || req.Argv[0] == "" {
		return execResult{ExitCode: -1, Err: "no command given"}
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defExecTimeout
	}
	if timeout > maxExecTimeout {
		timeout = maxExecTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
	cmd.Dir = defaultDir
	if req.Cwd != "" {
		d, err := confine(defaultDir, req.Cwd)
		if err != nil {
			return execResult{ExitCode: -1, Err: err.Error()}
		}
		cmd.Dir = d
	}
	// Own process group, killed as a whole: a timeout must not leave children running.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	if len(req.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}
	out, errw := &capWriter{max: maxExecOutput}, &capWriter{max: maxExecOutput}
	cmd.Stdout, cmd.Stderr = out, errw

	err := cmd.Run()
	res := execResult{Stdout: out.buf.Bytes(), Stderr: errw.buf.Bytes(), Truncated: out.truncated || errw.truncated}
	var ee *exec.ExitError
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.TimedOut, res.ExitCode = true, -1
	case err == nil:
	case errors.As(err, &ee):
		res.ExitCode = ee.ExitCode()
	default:
		res.ExitCode, res.Err = -1, err.Error()
	}
	return res
}

// confine resolves p (relative to base, or absolute) and refuses anything outside base,
// including paths that escape through "..", or through a symlink that already exists.
func confine(base, p string) (string, error) {
	baseReal, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("workspace directory unavailable: %v", err)
	}
	full := p
	if !filepath.IsAbs(p) {
		full = filepath.Join(base, p)
	}
	full = filepath.Clean(full)
	// Resolve symlinks on the longest part that exists, then re-attach the rest.
	existing, rest := full, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q: %v", p, err)
	}
	resolved := filepath.Join(real, rest)
	if resolved != baseReal && !strings.HasPrefix(resolved, baseReal+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q is outside the workspace directory", p)
	}
	return resolved, nil
}

func readFile(base, p string) ([]byte, error) {
	path, err := confine(base, p)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%q is a directory", p)
	}
	if st.Size() > maxFileTransfer {
		return nil, fmt.Errorf("%q is %d bytes; the limit is %d", p, st.Size(), maxFileTransfer)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxFileTransfer+1))
}

func writeFile(base, p string, data []byte, mode os.FileMode) error {
	if len(data) > maxFileTransfer {
		return fmt.Errorf("file is %d bytes; the limit is %d", len(data), maxFileTransfer)
	}
	path, err := confine(base, p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Re-check after creating directories: the parent now exists and may be a symlink.
	if path, err = confine(base, p); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	return os.WriteFile(path, data, mode&0o777)
}

// ─── message wrappers ─────────────────────────────────────────────────────────

func decodeB64(s string) []byte { b, _ := base64.StdEncoding.DecodeString(s); return b }

func handleExecMsg(send func(Message), msg Message, dir string) {
	res := runExec(execRequest{
		Argv: msg.Cmd, Cwd: msg.Cwd, Stdin: decodeB64(msg.Data),
		Timeout: time.Duration(msg.TimeoutS) * time.Second,
	}, dir)
	send(Message{
		Type: "exec_res", ID: msg.ID, Exit: res.ExitCode, TimedOut: res.TimedOut, Truncated: res.Truncated,
		Stdout: base64.StdEncoding.EncodeToString(res.Stdout), Stderr: base64.StdEncoding.EncodeToString(res.Stderr), Err: res.Err,
	})
}

func handleFsMsg(send func(Message), msg Message, dir string) {
	out := Message{Type: "fs_res", ID: msg.ID}
	switch msg.Type {
	case "fs_read":
		data, err := readFile(dir, msg.Path)
		if err != nil {
			out.Err = err.Error()
		} else {
			out.Data = base64.StdEncoding.EncodeToString(data)
		}
	case "fs_write":
		if err := writeFile(dir, msg.Path, decodeB64(msg.Data), os.FileMode(msg.Mode)); err != nil {
			out.Err = err.Error()
		}
	}
	send(out)
}

// workspaceBase is the directory commands start in and files are confined to.
func workspaceBase() string {
	if d := os.Getenv("WORKSPACE_DIR"); d != "" {
		return d
	}
	return "/home/user"
}
