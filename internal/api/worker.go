package api

import (
	"context"
	"os"

	"github.com/igloo-dev/igloo/internal/worker"
)

// StartWorker starts the embedded job worker (concurrency=1).
// In production, the standalone provisioner service handles jobs instead,
// but we keep one worker in the API server for local dev and as a fallback.
func (h *Handler) StartWorker(ctx context.Context) {
	w := worker.New(h.Store, os.Getenv("IGLOO_SERVER_URL"))
	w.Start(ctx, 1)
}
