package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/igloo-dev/igloo/internal/provisiond"
)

func main() {
	serverURL := os.Getenv("IGLOO_SERVER_URL")
	if serverURL == "" {
		log.Fatal("[provisioner] IGLOO_SERVER_URL must be set")
	}

	psk := os.Getenv("IGLOO_PROVISIONER_KEY")
	if psk == "" {
		log.Fatal("[provisioner] IGLOO_PROVISIONER_KEY must be set")
	}

	concurrency := 10
	if c := os.Getenv("IGLOO_WORKER_CONCURRENCY"); c != "" {
		if n, err := strconv.Atoi(c); err == nil && n > 0 {
			concurrency = n
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := provisiond.New(serverURL, psk)

	// First signal: stop taking jobs and let running Terraform finish (up to
	// IGLOO_DRAIN_TIMEOUT; keep it below the pod's terminationGracePeriodSeconds).
	// A second signal stops immediately.
	drainTimeout := 25 * time.Minute
	if v := os.Getenv("IGLOO_DRAIN_TIMEOUT"); v != "" {
		if t, err := time.ParseDuration(v); err == nil && t >= 0 {
			drainTimeout = t
		}
	}
	go func() {
		sig := make(chan os.Signal, 2)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("[provisioner] shutting down: finishing running jobs (signal again to stop now)...")
		go func() {
			<-sig
			log.Println("[provisioner] second signal, stopping now")
			cancel()
		}()
		if d.Drain(drainTimeout) {
			log.Println("[provisioner] drained cleanly")
		} else {
			log.Printf("[provisioner] drain timed out after %v, stopping running jobs", drainTimeout)
		}
		cancel()
	}()

	d.Start(ctx, concurrency)

	log.Printf("[provisioner] running with %d workers", concurrency)
	<-ctx.Done()
}
