package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/igloo-dev/igloo/internal/provisiond"
)

func main() {
	serverURL := os.Getenv("IGLOO_SERVER_URL")
	if serverURL == "" {
		log.Fatal("[provisioner] IGLOO_SERVER_URL must be set")
	}

	psk := os.Getenv("IGLOO_PROVISIONER_PSK")
	if psk == "" {
		log.Fatal("[provisioner] IGLOO_PROVISIONER_PSK must be set")
	}

	concurrency := 10
	if c := os.Getenv("IGLOO_WORKER_CONCURRENCY"); c != "" {
		if n, err := strconv.Atoi(c); err == nil && n > 0 {
			concurrency = n
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("[provisioner] shutting down...")
		cancel()
	}()

	d := provisiond.New(serverURL, psk)
	d.Start(ctx, concurrency)

	log.Printf("[provisioner] running with %d workers", concurrency)
	<-ctx.Done()
}
