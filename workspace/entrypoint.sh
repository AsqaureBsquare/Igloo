#!/bin/bash
set -e

WORKSPACE=/workspace

# First-run initialization (EBS volume is freshly formatted)
if [ ! -f "$WORKSPACE/.igloo-initialized" ]; then
    echo "[igloo] Initializing workspace..."
    mkdir -p "$WORKSPACE"/{projects,home,.config,.ssh}
    chmod 700 "$WORKSPACE/.ssh"
    echo "Initialized at $(date -u)" > "$WORKSPACE/.igloo-initialized"
    echo "[igloo] Workspace initialized."
fi

echo "[igloo] Workspace ready. Waiting for ECS Exec sessions..."

# Keep the container alive. ECS Exec sessions are opened by the ECS agent
# using the SSM session manager — no open ports required.
exec sleep infinity
