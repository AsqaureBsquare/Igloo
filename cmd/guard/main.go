// igloo-guard is the egress gateway for guarded workspaces: all of a workspace's outbound
// traffic goes through it, it enforces the workspace's network policy, adds credentials the
// workspace never holds, and reports every decision to the Igloo server.
//
//	IGLOO_SERVER_URL         where the Igloo server is (required)
//	IGLOO_GUARD_KEY          shared secret for the server's /api/guard API (required)
//	IGLOO_GUARD_ADDR         listen address (default :3128)
//	IGLOO_GUARD_SECRETS_DIR  directory with one file per secret; a route's secret name is
//	                         the file name (default /etc/igloo-guard/secrets)
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/igloo-dev/igloo/internal/guard"
)

var secretName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// fileSecrets reads a secret fresh on every use, so rotating a Kubernetes Secret takes
// effect without restarting the gateway. Names are checked so a route cannot name a path.
func fileSecrets(dir string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if !secretName.MatchString(name) || strings.Contains(name, "..") {
			return "", false
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", false
		}
		v := strings.TrimRight(string(b), "\r\n")
		return v, v != ""
	}
}

func main() {
	serverURL, key := os.Getenv("IGLOO_SERVER_URL"), os.Getenv("IGLOO_GUARD_KEY")
	if serverURL == "" || key == "" {
		log.Fatal("[guard] IGLOO_SERVER_URL and IGLOO_GUARD_KEY must be set")
	}
	addr := os.Getenv("IGLOO_GUARD_ADDR")
	if addr == "" {
		addr = ":3128"
	}
	dir := os.Getenv("IGLOO_GUARD_SECRETS_DIR")
	if dir == "" {
		dir = "/etc/igloo-guard/secrets"
	}

	client := &guard.ServerClient{BaseURL: serverURL, Key: key}
	sink := guard.NewBatchSink(client.SendEvents, time.Second, 100, 20000)
	gw := &guard.Gateway{Auth: client, Secrets: fileSecrets(dir), Sink: sink}

	srv := &http.Server{
		Addr:              addr,
		Handler:           gw,
		ReadHeaderTimeout: 15 * time.Second, // no overall read/write timeout: tunnels are long-lived
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		log.Printf("[guard] listening on %s (server %s, secrets %s)", addr, serverURL, dir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[guard] %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("[guard] shutting down: finishing requests, delivering the audit trail")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	sink.Close()
	log.Printf("[guard] bye (denied %d request(s) this run, %d audit event(s) dropped)", gw.Denied(), sink.Dropped())
}
