#!/bin/bash
set -e

WORKSPACE="${WORKSPACE_DIR:-/home/user}"

# First-run initialization (PVC is freshly provisioned)
if [ ! -f "$WORKSPACE/.igloo-initialized" ]; then
    echo "[igloo] Initializing workspace..."
    mkdir -p "$WORKSPACE"/{projects,.config,.ssh}
    chmod 700 "$WORKSPACE/.ssh"
    echo "Initialized at $(date -u)" > "$WORKSPACE/.igloo-initialized"
    echo "[igloo] Workspace initialized."
fi

# Generate persistent SSH host keys on PVC (survives sleep/wake)
if [ ! -f "$WORKSPACE/.ssh/ssh_host_ed25519_key" ]; then
    ssh-keygen -q -t ed25519 -f "$WORKSPACE/.ssh/ssh_host_ed25519_key" -N ""
    ssh-keygen -q -t rsa -b 4096 -f "$WORKSPACE/.ssh/ssh_host_rsa_key" -N ""
fi

# Inject user's SSH public key from server (set at workspace launch)
if [ -n "${IGLOO_SSH_KEY:-}" ]; then
    echo "${IGLOO_SSH_KEY}" > "$WORKSPACE/.ssh/authorized_keys"
    chmod 600 "$WORKSPACE/.ssh/authorized_keys"
    echo "[igloo] SSH public key installed."
else
    echo "[igloo] WARNING: no SSH key provided — SSH will reject all connections."
fi

mkdir -p /run/sshd

/usr/sbin/sshd -D -e -f /etc/ssh/igloo_sshd_config &
SSHD_PID=$!
sleep 0.5
if kill -0 $SSHD_PID 2>/dev/null; then
    echo "[igloo] sshd started (pid=$SSHD_PID)."
else
    echo "[igloo] ERROR: sshd failed to start — SSH will not work."
fi

echo "[igloo] Workspace ready."

if [ -x /usr/local/bin/igloo-agent ] && [ -n "${IGLOO_SERVER_URL:-}" ]; then
    echo "[igloo] Starting igloo-agent..."
    /usr/local/bin/igloo-agent &
else
    echo "[igloo] igloo-agent not started (binary absent or IGLOO_SERVER_URL not set)."
fi

exec sleep infinity
