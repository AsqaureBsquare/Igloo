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
	"runtime"
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
	case "templates", "tpl":
		runTemplates(os.Args[2:])
	case "token", "tokens":
		runToken(os.Args[2:])
	case "users":
		runUsers(os.Args[2:])
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
  igloo login <server-url>                  Authenticate via browser and save session
  igloo ssh <workspace-name> [ssh-flags]   SSH into a workspace
  igloo config-ssh                          Write SSH config for direct ssh use
  igloo proxy <workspace-name|id>           SSH ProxyCommand (used by config-ssh)
  igloo provisioner-keys <subcommand>       Manage provisioner keys (admin)
  igloo templates <subcommand>              Manage workspace templates
  igloo token <subcommand>                  Manage API tokens
  igloo users <subcommand>                  Manage users (admin)
  igloo version                             Print version

API token subcommands:
  igloo token create --name <n> [--lifetime 8760h] [--user <email>]
  igloo token list
  igloo token revoke <id>

User subcommands:
  igloo users create --email <e> --name <n> [--login-type none]

Environment variables (for CI / service accounts):
  IGLOO_TOKEN       API token (bypasses session file)
  IGLOO_SERVER_URL  Server URL (required when IGLOO_TOKEN is set)

SSH:
  igloo login https://igloo.example.com
  igloo ssh my-workspace         # WireGuard tailnet (recommended)
  igloo config-ssh               # write ~/.ssh/config for native ssh

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
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: igloo login <server-url>")
		os.Exit(1)
	}
	serverURL := strings.TrimRight(args[0], "/")
	code := generateDeviceCode()

	// Register the code with the server
	body, _ := json.Marshal(map[string]string{"code": code})
	resp, err := http.Post(serverURL+"/api/auth/device/init", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusOK {
		log.Fatalf("failed to reach server: %v", err)
	}
	resp.Body.Close()

	authURL := serverURL + "/cli-auth?code=" + code
	fmt.Fprintf(os.Stderr, "\n  Your one-time code:  %s\n\n", code)
	fmt.Fprintf(os.Stderr, "  Opening: %s\n\n", authURL)
	openBrowser(authURL)
	fmt.Fprintln(os.Stderr, "  Waiting for browser approval...")

	// Poll until approved or expired
	token := pollDeviceCode(serverURL, code)

	if err := saveSession(&session{ServerURL: serverURL, Token: token}); err != nil {
		log.Fatalf("failed to save session: %v", err)
	}
	fmt.Fprintln(os.Stderr, "\n  ✓ Logged in successfully")
	fmt.Fprintln(os.Stderr, "  Run `igloo config-ssh` to enable `ssh igloo-<workspace>`")
}

func pollDeviceCode(serverURL, code string) string {
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		resp, err := client.Get(serverURL + "/api/auth/device/poll?code=" + code)
		if err != nil {
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusGone {
			fmt.Fprintln(os.Stderr, "Code expired. Run igloo login again.")
			os.Exit(1)
		}
		if resp.StatusCode == http.StatusOK {
			var result map[string]string
			json.NewDecoder(resp.Body).Decode(&result)
			return result["token"]
		}
		// 202 = still pending, keep polling
	}
	fmt.Fprintln(os.Stderr, "Timed out waiting for approval.")
	os.Exit(1)
	return ""
}

// generateDeviceCode returns a code like BCDF-GHKM (no ambiguous chars).
func generateDeviceCode() string {
	const chars = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "  Could not open browser. Open manually:\n  %s\n\n", url)
	}
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
  Port 2222
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
	port := "2222"
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
	q.Set("port", port)
	u.RawQuery = q.Encode()

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), http.Header{"Authorization": {"Bearer " + fwToken}})
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
	} else if dbURL := os.Getenv("IGLOO_DATABASE_URL"); dbURL != "" {
		pg, err := store.NewPostgresStore(context.Background(), dbURL)
		if err != nil {
			log.Fatalf("[igloo] postgres init failed: %v", err)
		}
		log.Printf("[igloo] using postgres store")
		st = pg
	} else if mongoURL := os.Getenv("IGLOO_MONGO_URL"); mongoURL != "" {
		mg, err := store.NewMongoStore(context.Background(), mongoURL)
		if err != nil {
			log.Fatalf("[igloo] mongo init failed: %v", err)
		}
		log.Printf("[igloo] using mongo store")
		st = mg
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
	// IGLOO_TOKEN + IGLOO_SERVER_URL env vars bypass the session file (CI / service accounts).
	if tok := os.Getenv("IGLOO_TOKEN"); tok != "" {
		url := os.Getenv("IGLOO_SERVER_URL")
		if url == "" {
			fmt.Fprintln(os.Stderr, "IGLOO_SERVER_URL must be set when using IGLOO_TOKEN")
			os.Exit(1)
		}
		return &session{ServerURL: strings.TrimRight(url, "/"), Token: tok}
	}
	sess, err := loadSession()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Not logged in. Run: igloo login <server-url>")
		os.Exit(1)
	}
	return sess
}

// ─── templates subcommand ─────────────────────────────────────────────────────

func runTemplates(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: igloo templates <push|ls|rm>")
		os.Exit(1)
	}
	switch args[0] {
	case "push":
		runTemplatesPush(args[1:])
	case "ls", "list":
		runTemplatesList()
	case "rm", "delete", "remove":
		runTemplatesDelete(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

// igloo templates push <dir> [--name <name>] [--id <id>] [--provider <provider>]
func runTemplatesPush(args []string) {
	var dir, name, id, provider, description, icon string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 < len(args) {
				name = args[i+1]
				i++
			}
		case "--id":
			if i+1 < len(args) {
				id = args[i+1]
				i++
			}
		case "--provider":
			if i+1 < len(args) {
				provider = args[i+1]
				i++
			}
		case "--description":
			if i+1 < len(args) {
				description = args[i+1]
				i++
			}
		case "--icon":
			if i+1 < len(args) {
				icon = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "--") && dir == "" {
				dir = args[i]
			}
		}
	}
	if dir == "" {
		fmt.Fprintln(os.Stderr, "Usage: igloo templates push <dir> [--name <name>] [--id <id>] [--provider <provider>]")
		os.Exit(1)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		log.Fatalf("invalid path: %v", err)
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		log.Fatalf("cannot read directory %s: %v", absDir, err)
	}

	files := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".tf" && ext != ".tfvars" && ext != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(absDir, e.Name()))
		if err != nil {
			log.Fatalf("read %s: %v", e.Name(), err)
		}
		files[e.Name()] = string(data)
	}
	if len(files) == 0 {
		log.Fatalf("no .tf files found in %s", absDir)
	}

	if name == "" {
		name = filepath.Base(absDir)
	}
	if provider == "" {
		provider = "kubernetes"
	}

	sess := mustLoadSession()

	// Auto-detect existing template by name if no --id given.
	if id == "" {
		req, _ := http.NewRequest("GET", sess.ServerURL+"/api/templates", nil)
		req.Header.Set("Authorization", "Bearer "+sess.Token)
		if resp, err := (&http.Client{}).Do(req); err == nil {
			defer resp.Body.Close()
			var list []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if json.NewDecoder(resp.Body).Decode(&list) == nil {
				for _, t := range list {
					if t.Name == name {
						id = t.ID
						break
					}
				}
			}
		}
	}

	// If id resolved, update that template; otherwise create a new one.
	var method, url string
	if id != "" {
		method = "PUT"
		url = sess.ServerURL + "/api/templates/" + id
	} else {
		method = "POST"
		url = sess.ServerURL + "/api/templates"
	}

	payload := map[string]any{
		"name":        name,
		"provider":    provider,
		"description": description,
		"icon":        icon,
		"files":       files,
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		fmt.Fprintf(os.Stderr, "Error: %v\n", result["error"])
		os.Exit(1)
	}

	action := "updated"
	if method == "POST" {
		action = "created"
	}
	fmt.Printf("Template %s\n", action)
	fmt.Printf("  ID:       %v\n", result["id"])
	fmt.Printf("  Name:     %v\n", result["name"])
	fmt.Printf("  Provider: %v\n", result["provider"])
	fmt.Printf("  Version:  %v\n", result["version"])
	fmt.Printf("  Files:    %d uploaded\n", len(files))
}

func runTemplatesList() {
	sess := mustLoadSession()
	req, _ := http.NewRequest("GET", sess.ServerURL+"/api/templates", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var templates []map[string]any
	json.NewDecoder(resp.Body).Decode(&templates)

	if len(templates) == 0 {
		fmt.Println("No templates.")
		return
	}

	fmt.Printf("%-36s  %-20s  %-15s  %s\n", "ID", "NAME", "PROVIDER", "VERSION")
	fmt.Println(strings.Repeat("-", 80))
	for _, t := range templates {
		ver := "1"
		if v, ok := t["version"]; ok {
			ver = fmt.Sprintf("%.0f", v)
		}
		fmt.Printf("%-36s  %-20s  %-15s  %s\n", t["id"], t["name"], t["provider"], ver)
	}
}

func runTemplatesDelete(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: igloo templates rm <id>")
		os.Exit(1)
	}
	id := args[0]
	sess := mustLoadSession()
	req, _ := http.NewRequest("DELETE", sess.ServerURL+"/api/templates/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		fmt.Printf("Template %s deleted.\n", id)
	} else {
		fmt.Fprintf(os.Stderr, "Failed to delete template (HTTP %d)\n", resp.StatusCode)
		os.Exit(1)
	}
}

// ─── token subcommand ─────────────────────────────────────────────────────────

func runToken(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: igloo token <create|list|revoke>")
		os.Exit(1)
	}
	switch args[0] {
	case "create", "new":
		runTokenCreate(args[1:])
	case "list", "ls":
		runTokenList()
	case "revoke", "rm", "delete":
		runTokenRevoke(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func runTokenCreate(args []string) {
	var name, lifetime, userEmail string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if i+1 < len(args) {
				name = args[i+1]
				i++
			}
		case "--lifetime":
			if i+1 < len(args) {
				lifetime = args[i+1]
				i++
			}
		case "--user":
			if i+1 < len(args) {
				userEmail = args[i+1]
				i++
			}
		}
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "Usage: igloo token create --name <name> [--lifetime 8760h] [--user <email>]")
		os.Exit(1)
	}

	sess := mustLoadSession()

	// If --user supplied, look up user ID first.
	userID := ""
	if userEmail != "" {
		// GET /api/admin/users to find the user by email
		req, _ := http.NewRequest("GET", sess.ServerURL+"/api/admin/users", nil)
		req.Header.Set("Authorization", "Bearer "+sess.Token)
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			log.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		var users []map[string]any
		json.NewDecoder(resp.Body).Decode(&users)
		for _, u := range users {
			if e, _ := u["email"].(string); strings.EqualFold(e, userEmail) {
				userID, _ = u["id"].(string)
				break
			}
		}
		if userID == "" {
			log.Fatalf("user %q not found", userEmail)
		}
	}

	body := map[string]any{"name": name}
	if lifetime != "" {
		body["lifetime"] = lifetime
	}
	if userID != "" {
		body["user_id"] = userID
	}

	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", sess.ServerURL+"/api/tokens", bytes.NewReader(data))
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

	fmt.Printf("Token created\n")
	fmt.Printf("  ID:      %s\n", result["id"])
	fmt.Printf("  Name:    %s\n", result["name"])
	fmt.Printf("  Token:   %s\n", result["token"])
	fmt.Printf("\nSave the token — it will not be shown again.\n")
	fmt.Printf("Use it with: IGLOO_TOKEN=%s IGLOO_SERVER_URL=%s igloo ...\n", result["token"], sess.ServerURL)
}

func runTokenList() {
	sess := mustLoadSession()
	req, _ := http.NewRequest("GET", sess.ServerURL+"/api/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var tokens []map[string]any
	json.NewDecoder(resp.Body).Decode(&tokens)

	if len(tokens) == 0 {
		fmt.Println("No API tokens.")
		return
	}

	fmt.Printf("%-36s  %-20s  %-12s  %s\n", "ID", "NAME", "PREFIX", "EXPIRES")
	fmt.Println(strings.Repeat("-", 90))
	for _, t := range tokens {
		fmt.Printf("%-36s  %-20s  %-12s  %s\n",
			t["id"], t["name"], t["token_prefix"], t["expires_at"])
	}
}

func runTokenRevoke(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: igloo token revoke <id>")
		os.Exit(1)
	}
	id := args[0]
	sess := mustLoadSession()
	req, _ := http.NewRequest("DELETE", sess.ServerURL+"/api/tokens/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		fmt.Printf("Token %s revoked.\n", id)
	} else {
		fmt.Fprintf(os.Stderr, "Failed to revoke token (HTTP %d)\n", resp.StatusCode)
		os.Exit(1)
	}
}

// ─── users subcommand ─────────────────────────────────────────────────────────

func runUsers(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: igloo users <create>")
		os.Exit(1)
	}
	switch args[0] {
	case "create", "new":
		runUsersCreate(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func runUsersCreate(args []string) {
	var email, name, loginType string
	loginType = "none"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--email":
			if i+1 < len(args) {
				email = args[i+1]
				i++
			}
		case "--name":
			if i+1 < len(args) {
				name = args[i+1]
				i++
			}
		case "--login-type":
			if i+1 < len(args) {
				loginType = args[i+1]
				i++
			}
		}
	}
	if email == "" || name == "" {
		fmt.Fprintln(os.Stderr, "Usage: igloo users create --email <email> --name <name> [--login-type none]")
		os.Exit(1)
	}

	sess := mustLoadSession()
	data, _ := json.Marshal(map[string]string{"email": email, "name": name, "login_type": loginType})
	req, _ := http.NewRequest("POST", sess.ServerURL+"/api/admin/users", bytes.NewReader(data))
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

	fmt.Printf("User created\n")
	fmt.Printf("  ID:         %s\n", result["id"])
	fmt.Printf("  Email:      %s\n", result["email"])
	fmt.Printf("  Name:       %s\n", result["name"])
	fmt.Printf("  Login type: %s\n", result["login_type"])
	fmt.Printf("\nCreate a token for this account:\n")
	fmt.Printf("  igloo token create --name ci --user %s\n", email)
}
