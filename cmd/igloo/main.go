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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/auth"
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
	case "proxy":
		runProxy(os.Args[2:])
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
  igloo server [--port 8080] [--local]   Start the control plane
  igloo login <server-url> <email>        Authenticate and save credentials
  igloo proxy <workspace-id>             SSH ProxyCommand — pipe stdin/stdout to workspace port 22
  igloo version                           Print version

SSH setup:
  1. igloo login http://your-server.com you@example.com
  2. Add to ~/.ssh/config:
       Host igloo-*
         ProxyCommand igloo proxy %h
         User root
         StrictHostKeyChecking no
  3. ssh igloo-<workspace-id>

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
	srv.StartWorker(ctx)

	addr := ":" + port
	log.Printf("[igloo] server starting on http://localhost%s", addr)
	log.Printf("[igloo] open your browser → http://localhost%s", addr)

	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatal(err)
	}
}
