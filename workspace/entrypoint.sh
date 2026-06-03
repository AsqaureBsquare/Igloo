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

# Generate persistent SSH host keys on EBS (survives sleep/wake)
if [ ! -f "$WORKSPACE/.ssh/ssh_host_ed25519_key" ]; then
    ssh-keygen -q -t ed25519 -f "$WORKSPACE/.ssh/ssh_host_ed25519_key" -N ""
    ssh-keygen -q -t rsa -b 4096 -f "$WORKSPACE/.ssh/ssh_host_rsa_key" -N ""
fi

# Inject user's SSH public key from server (set at workspace launch)
if [ -n "${IGLOO_SSH_KEY:-}" ]; then
    echo "${IGLOO_SSH_KEY}" > "$WORKSPACE/.ssh/authorized_keys"
    chmod 600 "$WORKSPACE/.ssh/authorized_keys"
    echo "[igloo] SSH public key installed."
fi

# Start sshd
/usr/sbin/sshd -f /etc/ssh/igloo_sshd_config
echo "[igloo] sshd started."

echo "[igloo] Workspace ready."

# Start igloo-agent in the background if the binary is present and configured.
# The agent dials outbound to API Gateway WebSocket so browsers can open a
# PTY terminal without any inbound ports on the container.
if [ -x /usr/local/bin/igloo-agent ] && [ -n "${IGLOO_SERVER_URL:-}" ]; then
    echo "[igloo] Starting igloo-agent..."
    /usr/local/bin/igloo-agent &
else
    echo "[igloo] igloo-agent not started (binary absent or IGLOO_WS_URL not set)."
fi

exec sleep infinity
