// tntest — non-interactive tailnet smoke test.
// Usage: tntest <workspace-name>
// Reads ~/.igloo/session.json for server URL + JWT.
// Connects to the tailnet coordinator as a client, dials SSH on the WG IP,
// and reports whether the WireGuard handshake succeeded.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/igloo-dev/igloo/internal/tailnet"
)

type session struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: tntest <workspace-name>")
		os.Exit(1)
	}
	name := os.Args[1]

	home, _ := os.UserHomeDir()
	raw, err := os.ReadFile(home + "/.igloo/session.json")
	if err != nil {
		log.Fatalf("read session: %v", err)
	}
	var sess session
	if err := json.Unmarshal(raw, &sess); err != nil {
		log.Fatalf("parse session: %v", err)
	}

	wsID := resolveWorkspace(sess, name)
	fwToken := getForwardToken(sess, wsID)

	fmt.Printf("[tntest] workspace=%s id=%s\n", name, wsID)
	fmt.Printf("[tntest] connecting to tailnet coordinator...\n")

	node, err := tailnet.Connect(sess.ServerURL, wsID, fwToken, "client")
	if err != nil {
		log.Fatalf("[tntest] tailnet.Connect: %v", err)
	}
	defer node.Close()

	fmt.Printf("[tntest] connected — my WG IP=%s  peer IP=%s\n", node.MyIP, node.PeerIP)
	fmt.Printf("[tntest] dialing SSH on peer (30s timeout)...\n")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := node.DialSSH(ctx)
	if err != nil {
		fmt.Printf("[tntest] FAIL — WireGuard handshake timed out: %v\n", err)
		os.Exit(1)
	}
	conn.Close()
	fmt.Printf("[tntest] SUCCESS — WireGuard tunnel is UP, SSH port reachable\n")
}

func resolveWorkspace(sess session, name string) string {
	req, _ := http.NewRequest("GET", sess.ServerURL+"/api/workspaces", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("list workspaces: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var list []map[string]any
	if err := json.Unmarshal(body, &list); err != nil {
		log.Fatalf("parse workspaces: %v", err)
	}
	for _, ws := range list {
		if ws["name"] == name {
			return ws["id"].(string)
		}
	}
	log.Fatalf("workspace %q not found", name)
	return ""
}

func getForwardToken(sess session, wsID string) string {
	req, _ := http.NewRequest("GET", sess.ServerURL+"/api/workspaces/"+wsID+"/forward-token", nil)
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("get forward token: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var m map[string]string
	if err := json.Unmarshal(body, &m); err != nil {
		log.Fatalf("parse forward token: %v", err)
	}
	tok, ok := m["token"]
	if !ok || tok == "" {
		log.Fatalf("no token in response: %s", body)
	}
	return tok
}
