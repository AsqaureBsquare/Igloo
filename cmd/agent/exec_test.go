package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunExecCapturesOutputExitCodeCwdAndStdin(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)

	res := runExec(execRequest{Argv: []string{"sh", "-c", "echo out; echo err >&2; exit 3"}}, dir)
	if res.ExitCode != 3 || string(res.Stdout) != "out\n" || string(res.Stderr) != "err\n" || res.TimedOut || res.Err != "" {
		t.Errorf("result = %+v", res)
	}
	// Compare resolved paths: a temp dir may sit behind a symlink (/var -> /private/var on macOS).
	if res := runExec(execRequest{Argv: []string{"pwd"}}, dir); mustReal(t, strings.TrimSpace(string(res.Stdout))) != mustReal(t, dir) {
		t.Errorf("default directory = %q, want %q", res.Stdout, dir)
	}
	if res := runExec(execRequest{Argv: []string{"pwd"}, Cwd: "sub"}, dir); mustReal(t, strings.TrimSpace(string(res.Stdout))) != filepath.Join(mustReal(t, dir), "sub") {
		t.Errorf("cwd sub = %q", res.Stdout)
	}
	if res := runExec(execRequest{Argv: []string{"cat"}, Stdin: []byte("piped in")}, dir); string(res.Stdout) != "piped in" {
		t.Errorf("stdin not delivered: %q", res.Stdout)
	}
}

func mustReal(t *testing.T, p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunExecRefusesBadRequests(t *testing.T) {
	dir := t.TempDir()
	if res := runExec(execRequest{}, dir); res.Err == "" || res.ExitCode != -1 {
		t.Errorf("empty command: %+v", res)
	}
	if res := runExec(execRequest{Argv: []string{"definitely-not-a-program-igloo"}}, dir); res.Err == "" || res.ExitCode != -1 {
		t.Errorf("missing program should report an error, not an exit code: %+v", res)
	}
	if res := runExec(execRequest{Argv: []string{"pwd"}, Cwd: "../.."}, dir); res.Err == "" {
		t.Errorf("a working directory outside the workspace must be refused: %+v", res)
	}
}

// A timeout must stop the command and everything it started, or a sandbox slowly fills with
// stray processes.
func TestTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	res := runExec(execRequest{Argv: []string{"sh", "-c", "sleep 31.7 & sleep 31.7 & wait"}, Timeout: time.Second}, dir)
	if !res.TimedOut {
		t.Fatalf("not reported as timed out: %+v", res)
	}
	if took := time.Since(start); took > 6*time.Second {
		t.Errorf("took %v to stop a command with a 1 s timeout", took)
	}
	time.Sleep(300 * time.Millisecond)
	if out, _ := exec.Command("pgrep", "-f", "sleep 31.7").Output(); len(bytes.TrimSpace(out)) != 0 {
		t.Errorf("child processes survived the timeout: pids %s", out)
	}
}

func TestOutputIsCappedWithoutHangingOrFailing(t *testing.T) {
	dir := t.TempDir()
	// Roughly 20 MB of output with a 1 MiB cap.
	res := runExec(execRequest{Argv: []string{"sh", "-c", "yes 0123456789abcdef | head -c 20000000; echo done >&2"}, Timeout: 30 * time.Second}, dir)
	if len(res.Stdout) != maxExecOutput || !res.Truncated || res.TimedOut {
		t.Errorf("stdout %d bytes, truncated=%v, timedOut=%v; want exactly %d, true, false", len(res.Stdout), res.Truncated, res.TimedOut, maxExecOutput)
	}
	if string(res.Stderr) != "done\n" || res.ExitCode != 0 {
		t.Errorf("the command should still finish normally: exit %d stderr %q", res.ExitCode, res.Stderr)
	}
}

func TestTimeoutIsBounded(t *testing.T) {
	dir := t.TempDir()
	// A request for an absurd timeout is clamped, not honoured; a normal quick command is unaffected.
	if res := runExec(execRequest{Argv: []string{"true"}, Timeout: 100 * time.Hour}, dir); res.ExitCode != 0 || res.TimedOut {
		t.Errorf("true with a huge timeout: %+v", res)
	}
}

// ─── file access ──────────────────────────────────────────────────────────────

func TestFilesAreConfinedToTheWorkspaceDirectory(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("top secret"), 0o600)
	os.Symlink(outside, filepath.Join(base, "link"))                       // a directory link out
	os.Symlink(filepath.Join(outside, "secret"), filepath.Join(base, "f")) // a file link out
	os.WriteFile(filepath.Join(base, "ok.txt"), []byte("fine"), 0o644)

	for _, p := range []string{"../secret", "../../etc/passwd", "link/secret", "f", outside + "/secret", "/etc/passwd", "sub/../../x"} {
		if _, err := readFile(base, p); err == nil {
			t.Errorf("readFile(%q) succeeded; it is outside the workspace", p)
		}
		if err := writeFile(base, p, []byte("x"), 0o644); err == nil {
			t.Errorf("writeFile(%q) succeeded; it is outside the workspace", p)
		}
	}
	if b, err := os.ReadFile(filepath.Join(outside, "secret")); err != nil || string(b) != "top secret" {
		t.Errorf("a file outside the workspace was modified: %q, %v", b, err)
	}
	for _, p := range []string{"ok.txt", "./ok.txt", filepath.Join(base, "ok.txt")} {
		if b, err := readFile(base, p); err != nil || string(b) != "fine" {
			t.Errorf("readFile(%q) = %q, %v", p, b, err)
		}
	}
}

func TestWriteCreatesDirectoriesAndKeepsTheMode(t *testing.T) {
	base := t.TempDir()
	if err := writeFile(base, "a/b/c.sh", []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(base, "a/b/c.sh"))
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, %v; want 0755", st.Mode(), err)
	}
	// A setuid bit in the request is ignored.
	if err := writeFile(base, "suid", []byte("x"), 0o4755); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(base, "suid")); st.Mode()&os.ModeSetuid != 0 {
		t.Error("a setuid bit was honoured")
	}
	if err := writeFile(base, "default", []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(base, "default")); st.Mode().Perm() != 0o644 {
		t.Errorf("default mode = %v, want 0644", st.Mode().Perm())
	}
}

func TestFileSizeLimitsAndDirectories(t *testing.T) {
	base := t.TempDir()
	if err := writeFile(base, "big", make([]byte, maxFileTransfer+1), 0); err == nil {
		t.Error("a file over the limit was accepted")
	}
	if err := writeFile(base, "exact", make([]byte, maxFileTransfer), 0); err != nil {
		t.Errorf("a file exactly at the limit was refused: %v", err)
	}
	os.WriteFile(filepath.Join(base, "huge"), make([]byte, maxFileTransfer+1), 0o644)
	if _, err := readFile(base, "huge"); err == nil {
		t.Error("reading a file over the limit succeeded")
	}
	os.Mkdir(filepath.Join(base, "d"), 0o755)
	if _, err := readFile(base, "d"); err == nil {
		t.Error("reading a directory succeeded")
	}
	if _, err := readFile(base, "missing"); err == nil {
		t.Error("reading a missing file succeeded")
	}
}
