package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/auth"
	igloo_tailnet "github.com/igloo-dev/igloo/internal/tailnet"
	"github.com/igloo-dev/igloo/internal/server"
	"github.com/igloo-dev/igloo/internal/store"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "server":
		runServer(os.Args[2:])
	case "login":
		runLogin(os.Args[2:])
	case "ssh":
		runSSH(os.Args[2:])
	case "config-ssh":
		runConfigSSH(os.Args[2:])
	case "proxy":
		runProxy(os.Args[2:])
	case "provisioner-keys", "pkeys":
		runProvisionerKeys(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("igloo %s\n", version)
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`igloo — serverless dev workspaces

Usage:
  igloo server [--port 8080] [--local]     Start the control plane
  igloo login <server-url> <email>          Authenticate and save credentials
  igloo ssh <workspace-name> [ssh-flags]   SSH into a workspace
  igloo config-ssh                          Write SSH config for direct ssh use
  igloo proxy <workspace-name|id>           SSH ProxyCommand (used by config-ssh)
  igloo provisioner-keys <subcommand>       Manage provisioner keys (admin)
  igloo version                             Print version

SSH (two options):
  # Option 1 — igloo ssh (recommended, works with or without Tailscale)
  igloo login https://igloo.example.com you@example.com
  igloo ssh my-workspace

  # Option 2 — native ssh via ProxyCommand
  igloo config-ssh
  ssh igloo-my-workspace

  # Option 3 — Tailscale direct (fastest, no relay)
  # Requires Tailscale installed on your machine and TAILSCALE_API_KEY on server
  tailscale ssh igloo-my-workspace

Provisioner key subcommands:
  igloo pkeys new <name>   Create a key (alias: create)
  igloo pkeys ls           List keys (alias: list)
  igloo pkeys rm <id>      Revoke a key (alias: revoke, delete)

`)
}

// ─── CLI session (stored separately from server config) ───────────────────────

type session struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
}

func sessionPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".igloo", "session.json")
}

func loadSession() (*session, error) {
	data, err := os.ReadFile(sessionPath())
	if err != nil {
		return nil, err
	}
	var s session
	return &s, json.Unmarshal(data, &s)
}

func saveSession(s *session) error {
	_ = os.MkdirAll(filepath.Dir(sessionPath()), 0700)
	data, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(sessionPath(), data, 0600)
}

// ─── login subcommand ─────────────────────────────────────────────────────────

func runLogin(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: igloo login <server-url> <email>")
		os.Exit(1)
	}
	serverURL := strings.TrimRight(args[0], "/")
	email := args[1]

	// Disable terminal echo for password input
	exec.Command("stty", "-echo").Run()
	fmt.Fprint(os.Stderr, "Password: ")
	var password string
	fmt.Fscanln(os.Stdin, &password)
	exec.Command("stty", "echo").Run()
	fmt.Fprintln(os.Stderr)

	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	resp, err := http.Post(serverURL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatalf("login failed: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var result map[string]string
	json.Unmarshal(raw, &result)
	token := result["token"]
	if token == "" {
		fmt.Fprintf(os.Stderr, "Login failed: %s\n", raw)
		os.Exit(1)
	}

	if err := saveSession(&session{ServerURL: serverURL, Token: token}); err != nil {
		log.Fatalf("failed to save session: %v", err)
	}
	fmt.Fprintf(os.Stderr, "Logged in. Credentials saved to %s\n", sessionPath())
}

// ─── ssh subcommand ───────────────────────────────────────────────────────────

func runSSH(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: igloo ssh <workspace-name> [ssh-flags...]")
		os.Exit(1)
	}
	name := args[0]
	extra := args[1:]

	sess := mustLoadSession()
	ws := resolveWorkspace(sess, name)
	id := ws["id"]

	// Get a forward token — the tailnet coordinator uses the same token type
	fwToken := getForwardToken(sess, id)

	// Connect to Igloo's built-in WireGuard coordinator.
	// This sets up a userspace WireGuard tunnel between this machine and the
	// workspace — no Tailscale, no external service, no keys to manage.
	node, err := igloo_tailnet.Connect(sess.ServerURL, id, fwToken, "client")
	if err != nil {
		log.Fatalf("tailnet connect: %v", err)
	}
	defer node.Close()

	// Wait for WireGuard handshake — up to 30s to account for agent peer setup
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := node.DialSSH(ctx)
	if err != nil {
		node.Close() // stop WireGuard before relay so it doesn't spam stderr
		fmt.Fprintf(os.Stderr, "→ tailnet unavailable (%v), connecting via relay\n", err)
		runSSHViaProxy(sess, id, extra)
		return
	}
	conn.Close()

	fmt.Fprintf(os.Stderr, "→ connected via WireGuard tailnet (peer %s)\n", node.PeerIP)
	runSSHViaTailnet(node, extra)
}

func runSSHViaTailnet(node *igloo_tailnet.Node, extra []string) {
	// Start a local listener on a random port; forward connections through WG
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("local listen: %v", err)
	}
	defer ln.Close()

	localPort := ln.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			local, err := ln.Accept()
			if err != nil {
				return
			}
			go func(local net.Conn) {
				defer local.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				remote, err := node.DialSSH(ctx)
				if err != nil {
					return
				}
				defer remote.Close()
				done := make(chan struct{}, 2)
				go func() { io.Copy(local, remote); done <- struct{}{} }()
				go func() { io.Copy(remote, local); done <- struct{}{} }()
				<-done
			}(local)
		}
	}()

	sshArgs := []string{
		"-p", fmt.Sprintf("%d", localPort),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-l", "root",
	}
	sshArgs = append(sshArgs, extra...)
	sshArgs = append(sshArgs, "127.0.0.1")
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			os.Exit(e.ExitCode())
		}
	}
}

func runSSHViaProxy(sess *session, id string, extra []string) {
	fmt.Fprintln(os.Stderr, "→ connected via WebSocket relay")
	sshArgs := []string{
		"-o", "ProxyCommand=igloo proxy " + id,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-l", "root",
	}
	sshArgs = append(sshArgs, extra...)
	sshArgs = append(sshArgs, id)
	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			os.Exit(e.ExitCode())
		}
		log.Fatal(err)
	}
}

func getForwardToken(sess *session, workspaceID string) string {
	req, _ := http.NewRequest("GET", sess.ServerURL+"/api/workspaces/"+workspaceID+"/forward-token", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Fatalf("forward-token request failed: %v", err)
	}
	defer resp.Body.Close()
	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	if t := result["token"]; t != "" {
		return t
	}
	log.Fatal("failed to get forward token (is workspace running?)")
	return ""
}

// resolveWorkspace returns a map with at least "id", "name", and optionally
// "tailscale_hostname" from the workspace meta/vars.
func resolveWorkspace(sess *session, name string) map[string]string {
	req, _ := http.NewRequest("GET", sess.ServerURL+"/api/workspaces", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Fatalf("failed to list workspaces: %v", err)
	}
	defer resp.Body.Close()

	var workspaces []struct {
		ID   string            `json:"id"`
		Name string            `json:"name"`
		Meta map[string]string `json:"meta"`
		Vars map[string]string `json:"vars"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&workspaces); err != nil {
		log.Fatalf("failed to decode workspaces: %v", err)
	}

	lower := strings.ToLower(name)
	for _, w := range workspaces {
		if strings.ToLower(w.Name) == lower {
			result := map[string]string{"id": w.ID, "name": w.Name}
			if h := w.Vars["tailscale_hostname"]; h != "" {
				result["tailscale_hostname"] = h
			}
			return result
		}
	}
	log.Fatalf("no workspace named %q", name)
	return nil
}

// ─── config-ssh subcommand ────────────────────────────────────────────────────

const sshConfigBlock = `# BEGIN igloo
# Managed by "igloo config-ssh" — do not edit this block manually.
Host igloo-*
  ProxyCommand igloo proxy %h
  User root
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
# END igloo
`

func runConfigSSH(_ []string) {
	home, _ := os.UserHomeDir()
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0700)
	cfgPath := filepath.Join(home, ".ssh", "config")

	existing, _ := os.ReadFile(cfgPath)
	cleaned := removeSSHBlock(string(existing))
	out := strings.TrimRight(cleaned, "\n") + "\n\n" + sshConfigBlock
	if err := os.WriteFile(cfgPath, []byte(out), 0600); err != nil {
		log.Fatalf("write %s: %v", cfgPath, err)
	}
	fmt.Printf("SSH config updated: %s\n", cfgPath)
	fmt.Println("You can now connect with:")
	fmt.Println("  igloo ssh <workspace-name>")
	fmt.Println("  ssh igloo-<workspace-name>")
}

func removeSSHBlock(cfg string) string {
	lines := strings.Split(cfg, "\n")
	var out []string
	skip := false
	for _, l := range lines {
		if strings.TrimSpace(l) == "# BEGIN igloo" {
			skip = true
		}
		if !skip {
			out = append(out, l)
		}
		if skip && strings.TrimSpace(l) == "# END igloo" {
			skip = false
		}
	}
	return strings.Join(out, "\n")
}

// ─── proxy subcommand (SSH ProxyCommand) ─────────────────────────────────────

func runProxy(args []string) {
	port := "22"
	var workspaceID string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port", "-p":
			if i+1 < len(args) {
				port = args[i+1]
				i++
			}
		default:
			workspaceID = args[i]
		}
	}

	if workspaceID == "" {
		fmt.Fprintln(os.Stderr, "Usage: igloo proxy [--port 22] <workspace-id>")
		os.Exit(1)
	}

	// Strip "igloo-" prefix if used as SSH hostname
	workspaceID = strings.TrimPrefix(workspaceID, "igloo-")

	sess, err := loadSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Not logged in. Run: igloo login <server-url> <email>")
		os.Exit(1)
	}

	// If it doesn't look like a UUID, resolve name → ID
	if !strings.Contains(workspaceID, "-") || len(workspaceID) < 32 {
		workspaceID = resolveWorkspaceName(sess, workspaceID)
	}

	// Get a short-lived forward token from the API
	req, _ := http.NewRequest("GET", sess.ServerURL+"/api/workspaces/"+workspaceID+"/forward-token", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Fatalf("forward-token request failed: %v", err)
	}
	defer resp.Body.Close()
	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	fwToken := result["token"]
	if fwToken == "" {
		log.Fatalf("failed to get forward token (is workspace running?)")
	}

	// Connect to the WebSocket forward endpoint
	wsBase := strings.Replace(sess.ServerURL, "https://", "wss://", 1)
	wsBase = strings.Replace(wsBase, "http://", "ws://", 1)
	u, _ := url.Parse(wsBase + "/api/workspaces/" + workspaceID + "/forward")
	q := u.Query()
	q.Set("token", fwToken)
	q.Set("port", port)
	u.RawQuery = q.Encode()

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	// stdin → WebSocket
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				conn.WriteMessage(websocket.BinaryMessage, buf[:n])
			}
			if err != nil {
				conn.Close()
				return
			}
		}
	}()

	// WebSocket → stdout
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		os.Stdout.Write(data)
	}
}

// resolveWorkspaceName calls GET /api/workspaces and returns the ID for the
// workspace whose name matches (case-insensitive). Exits on error or no match.
func resolveWorkspaceName(sess *session, name string) string {
	req, _ := http.NewRequest("GET", sess.ServerURL+"/api/workspaces", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Fatalf("failed to list workspaces: %v", err)
	}
	defer resp.Body.Close()

	var workspaces []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&workspaces); err != nil {
		log.Fatalf("failed to decode workspaces: %v", err)
	}

	lower := strings.ToLower(name)
	for _, w := range workspaces {
		if strings.ToLower(w.Name) == lower {
			return w.ID
		}
	}
	log.Fatalf("no workspace named %q", name)
	return ""
}

// ─── Server config (used by runServer) ───────────────────────────────────────

type config struct {
	JWTSecret string `json:"jwt_secret"`
}

func loadOrCreateConfig() *config {
	// In production (ECS), inject secret via env var — no file needed.
	if s := os.Getenv("IGLOO_JWT_SECRET"); s != "" {
		return &config{JWTSecret: s}
	}

	home, _ := os.UserHomeDir()
	cfgPath := filepath.Join(home, ".igloo", "config.json")
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0700)

	data, err := os.ReadFile(cfgPath)
	if err == nil {
		var cfg config
		if json.Unmarshal(data, &cfg) == nil && cfg.JWTSecret != "" {
			return &cfg
		}
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		log.Fatal("failed to generate JWT secret:", err)
	}
	cfg := &config{JWTSecret: hex.EncodeToString(secret)}
	data, _ = json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(cfgPath, data, 0600)
	log.Printf("[igloo] generated new JWT secret → %s", cfgPath)
	return cfg
}

// ─── Server subcommand ────────────────────────────────────────────────────────

func runServer(args []string) {
	port := "8080"
	local := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port", "-p":
			if i+1 < len(args) {
				port = args[i+1]
				i++
			}
		case "--local":
			local = true
		}
	}

	// PORT env var overrides flag (standard for cloud platforms)
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	cfg := loadOrCreateConfig()
	a := auth.New(cfg.JWTSecret)

	var st store.Store
	if local {
		log.Println("[igloo] using in-memory store (--local mode)")
		st = store.NewMemory()
	} else {
		region := os.Getenv("AWS_REGION")
		if region == "" {
			region = "us-east-1"
		}
		ddb, err := store.NewDynamoStore(context.Background(), region)
		if err != nil {
			log.Fatalf("[igloo] DynamoDB init failed: %v", err)
		}
		log.Printf("[igloo] using DynamoDB store (region=%s)", region)
		st = ddb
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown on SIGINT / SIGTERM
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("[igloo] shutting down...")
		cancel()
	}()

	srv := server.New(st, a)
	srv.SeedBuiltinTemplates(ctx)
	srv.BootstrapProvisionerKey(ctx)
	srv.StartWorker(ctx)

	addr := ":" + port
	log.Printf("[igloo] server starting on http://localhost%s", addr)
	log.Printf("[igloo] open your browser → http://localhost%s", addr)

	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatal(err)
	}
}

// ─── provisioner-keys subcommand ─────────────────────────────────────────────

func runProvisionerKeys(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: igloo provisioner-keys <create|list|revoke>")
		os.Exit(1)
	}
	switch args[0] {
	case "create", "new":
		runPKCreate(args[1:])
	case "list", "ls":
		runPKList()
	case "revoke", "rm", "delete":
		runPKRevoke(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func runPKCreate(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: igloo provisioner-keys create <name> [--tag k=v]...")
		os.Exit(1)
	}
	name := args[0]
	tags := map[string]string{}
	for i := 1; i < len(args); i++ {
		if args[i] == "--tag" && i+1 < len(args) {
			parts := strings.SplitN(args[i+1], "=", 2)
			if len(parts) == 2 {
				tags[parts[0]] = parts[1]
			}
			i++
		}
	}

	sess := mustLoadSession()
	body, _ := json.Marshal(map[string]any{"name": name, "tags": tags})
	req, _ := http.NewRequest("POST", sess.ServerURL+"/api/admin/provisioner-keys", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)

	if resp.StatusCode != http.StatusCreated {
		fmt.Fprintf(os.Stderr, "Error: %v\n", result["error"])
		os.Exit(1)
	}

	fmt.Printf("Key created\n")
	fmt.Printf("  ID:    %s\n", result["id"])
	fmt.Printf("  Name:  %s\n", result["name"])
	fmt.Printf("  Token: %s\n", result["token"])
	fmt.Printf("\nSave the token — it will not be shown again.\n")
	fmt.Printf("Set on your provisioner: IGLOO_PROVISIONER_KEY=%s\n", result["token"])
}

func runPKList() {
	sess := mustLoadSession()
	req, _ := http.NewRequest("GET", sess.ServerURL+"/api/admin/provisioner-keys", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var keys []map[string]any
	json.NewDecoder(resp.Body).Decode(&keys)

	if len(keys) == 0 {
		fmt.Println("No provisioner keys.")
		return
	}

	fmt.Printf("%-36s  %-20s  %-10s  %s\n", "ID", "NAME", "CREATED BY", "LAST USED")
	fmt.Println(strings.Repeat("-", 90))
	for _, k := range keys {
		lastUsed := "never"
		if lu, ok := k["last_used_at"].(string); ok && lu != "" {
			lastUsed = lu
		}
		fmt.Printf("%-36s  %-20s  %-10s  %s\n",
			k["id"], k["name"], k["created_by"], lastUsed)
	}
}

func runPKRevoke(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: igloo provisioner-keys revoke <id>")
		os.Exit(1)
	}
	id := args[0]
	sess := mustLoadSession()
	req, _ := http.NewRequest("DELETE", sess.ServerURL+"/api/admin/provisioner-keys/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		fmt.Printf("Key %s revoked.\n", id)
	} else {
		fmt.Fprintf(os.Stderr, "Failed to revoke key (HTTP %d)\n", resp.StatusCode)
		os.Exit(1)
	}
}

func mustLoadSession() *session {
	sess, err := loadSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Not logged in. Run: igloo login <server-url> <email>")
		os.Exit(1)
	}
	return sess
}
