#!/usr/bin/env bash
# Build igloo-agent (linux/arm64) and the workspace Docker image.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "==> Building igloo-agent (linux/arm64)..."
GOOS=linux GOARCH=arm64 go build -o workspace/igloo-agent ./cmd/agent
echo "    workspace/igloo-agent  $(du -sh workspace/igloo-agent | cut -f1)"

echo "==> Building igloo-workspace:latest Docker image..."
docker build -t igloo-workspace:latest -f workspace/Dockerfile.local workspace/
echo "    Done."

echo ""
echo "Run the server:"
echo "  go run ./cmd/igloo server --local"
echo ""
echo "Then open http://localhost:8080"
