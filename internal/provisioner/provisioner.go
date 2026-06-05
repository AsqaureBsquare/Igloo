// Package provisioner runs Terraform to create and destroy workspace infrastructure.
// Each workspace gets its own temp directory, S3 state, and tfvars file.
package provisioner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Action string

const (
	ActionApply   Action = "apply"
	ActionDestroy Action = "destroy"
)

type Input struct {
	WorkspaceID string
	Files       map[string]string // filename → terraform source from template
	Vars        map[string]string // all variables: user-supplied + server-injected
	StateBucket string            // S3 bucket for terraform state
	StateRegion string
	LogPath     string       // file to append terraform output to
	LogFunc     func(string) // optional: called for each output line (for live streaming)
}

// Run executes terraform apply or terraform destroy for a workspace.
// Returns a map of terraform output values (empty on destroy).
func Run(ctx context.Context, in Input, action Action) (map[string]string, error) {
	dir, err := os.MkdirTemp("", "igloo-ws-"+in.WorkspaceID+"-*")
	if err != nil {
		return nil, fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(dir)

	// Write template source files
	for name, content := range in.Files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("write %s: %w", name, err)
		}
	}

	// Generate backend.tf (S3 state per workspace)
	backend := fmt.Sprintf(`terraform {
  backend "s3" {
    bucket = %q
    key    = "workspaces/%s/terraform.tfstate"
    region = %q
  }
}
`, in.StateBucket, in.WorkspaceID, in.StateRegion)
	if err := os.WriteFile(filepath.Join(dir, "backend.tf"), []byte(backend), 0644); err != nil {
		return nil, fmt.Errorf("write backend.tf: %w", err)
	}

	// Generate terraform.tfvars
	var tfvars strings.Builder
	for k, v := range in.Vars {
		tfvars.WriteString(fmt.Sprintf("%s = %q\n", k, v))
	}
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfvars"), []byte(tfvars.String()), 0600); err != nil {
		return nil, fmt.Errorf("write tfvars: %w", err)
	}

	// Open log file
	lf, err := os.OpenFile(in.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	defer lf.Close()

	logLine := func(s string) {
		fmt.Fprintln(lf, s)
		lf.Sync()
		log.Print(s)
	}

	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "terraform", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1", "TF_INPUT=false")
		logLine(fmt.Sprintf("[provisioner] terraform %s", strings.Join(args, " ")))

		if in.LogFunc != nil {
			pr, pw := io.Pipe()
			cmd.Stdout = io.MultiWriter(lf, pw)
			cmd.Stderr = io.MultiWriter(lf, pw)
			scanDone := make(chan struct{})
			go func() {
				defer close(scanDone)
				scanner := bufio.NewScanner(pr)
				for scanner.Scan() {
					in.LogFunc(scanner.Text())
				}
			}()
			err := cmd.Run()
			pw.Close()
			<-scanDone
			lf.Sync()
			if err != nil {
				return fmt.Errorf("terraform %s: %w", args[0], err)
			}
			return nil
		}

		cmd.Stdout = lf
		cmd.Stderr = lf
		if err := cmd.Run(); err != nil {
			lf.Sync()
			return fmt.Errorf("terraform %s: %w", args[0], err)
		}
		return nil
	}

	if err := run("init", "-no-color", "-reconfigure"); err != nil {
		return nil, err
	}

	switch action {
	case ActionApply:
		if err := run("apply", "-auto-approve", "-no-color"); err != nil {
			return nil, err
		}
		return readOutputs(ctx, dir, lf)

	case ActionDestroy:
		if err := run("destroy", "-auto-approve", "-no-color"); err != nil {
			return nil, err
		}
		return nil, nil

	default:
		return nil, fmt.Errorf("unknown action: %s", action)
	}
}

// readOutputs runs terraform output -json and returns the values.
func readOutputs(ctx context.Context, dir string, lf *os.File) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "terraform", "output", "-json")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1")
	raw, err := cmd.Output()
	if err != nil {
		return map[string]string{}, nil
	}

	var parsed map[string]struct {
		Value interface{} `json:"value"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return map[string]string{}, nil
	}

	out := make(map[string]string, len(parsed))
	for k, v := range parsed {
		switch val := v.Value.(type) {
		case string:
			out[k] = val
		default:
			b, _ := json.Marshal(v.Value)
			out[k] = string(b)
		}
	}
	return out, nil
}
