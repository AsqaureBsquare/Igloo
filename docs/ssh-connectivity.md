# SSH Connectivity

Igloo workspaces run in AWS Fargate containers inside a private subnet — no public IP, no inbound ports. SSH access is achieved by routing traffic through a WebSocket relay that both the client and the container connect to outbound.

## Architecture

```
Your laptop                 Igloo server (ECS/Lambda)      Fargate container
───────────                 ─────────────────────────      ─────────────────
ssh igloo-<name>
  │
  ▼
~/.ssh/config matches
Host igloo-*
  ProxyCommand /tmp/igloo proxy %h
  │
  ▼
igloo proxy <name>
  1. GET /api/workspaces → resolve name to UUID
  2. GET /api/workspaces/{id}/forward-token → short-lived JWT
  3. WSS /api/workspaces/{id}/forward?token=...&port=22
  │                               │
  │   sends tcp_open {port:22}    │
  │ ─────────────────────────────►│◄──WSS── igloo-agent
  │                               │         (dials home on startup)
  │                               │
  │   tcp_ready                   │         igloo-agent dials
  │ ◄─────────────────────────────│         127.0.0.1:22 (sshd)
  │                               │
  │◄──── bidirectional tcp_data ──│◄──────► sshd
  │      frames (base64 over WS)  │
  ▼
SSH negotiates keys,
opens shell — no idea
it's going through WS
```

## Components

### `~/.ssh/config` (client machine)
```
Host igloo-*
  ProxyCommand /tmp/igloo proxy %h
  User root
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
```

The `ProxyCommand` replaces the normal TCP socket. SSH writes to stdin and reads from stdout of the proxy process — it sees a transparent byte pipe.

### `igloo proxy` (`cmd/igloo/main.go`)
1. Resolves workspace name → UUID via `GET /api/workspaces` if the argument isn't already a UUID
2. Fetches a short-lived forward token (signed JWT, 1-hour TTL) from `GET /api/workspaces/{id}/forward-token`
3. Dials `wss://{server}/api/workspaces/{id}/forward?token=...&port=22`
4. Pipes `os.Stdin → WebSocket` and `WebSocket → os.Stdout` in a goroutine loop

### Server: `Forward` + `ForwardToken` (`internal/api/forward.go`)
- **`ForwardToken`**: requires Bearer auth, issues a JWT with `role=forward` and the workspace ID embedded. Short TTL (1hr) so it can be passed as a URL query param safely.
- **`Forward`**: validates the JWT from `?token=`, upgrades to WebSocket, sends `{type:"tcp_open", port:"22"}` to the igloo-agent via the in-memory relay, waits for `{type:"tcp_ready"}`, then relays `tcp_data` frames bidirectionally until either side closes.

### igloo-agent (`cmd/agent/main.go`)
- Dials `wss://{IGLOO_SERVER_URL}/ws?ticket=...` outbound on startup — no inbound ports needed.
- Handles `tcp_open`: calls `net.Dial("tcp", "127.0.0.1:"+port)`, stores the connection, sends `tcp_ready`.
- Handles `tcp_data`: decodes base64 payload, writes to the TCP connection.
- Handles `tcp_close`: closes the TCP connection and removes it from the map.
- Reads from the TCP connection in a goroutine, sending `tcp_data` frames back to the server.

### sshd (inside the Fargate container)
- Installed in the workspace Docker image (`workspace/Dockerfile`).
- Config at `/etc/ssh/igloo_sshd_config`: listens on port 22, root login allowed, password auth disabled, pubkey only.
- Host keys generated on first run into `/workspace/.ssh/` (EBS volume) — survive sleep/wake cycles.
- `authorized_keys` written by `workspace/entrypoint.sh` from the `IGLOO_SSH_KEY` environment variable injected at task launch.

## SSH Key Injection Flow

```
User saves SSH public key in Igloo UI
  → PUT /api/auth/me/ssh-key
  → stored in DynamoDB user record

User clicks Start on a workspace
  → server fetches user.SSHPublicKey
  → passes as IGLOO_SSH_KEY env var in ECS RunTask container overrides
  → entrypoint.sh writes it to /workspace/.ssh/authorized_keys
  → sshd picks it up on every connection (no restart needed)
```

## Message Protocol (WebSocket frames)

All messages are JSON:

| Type | Direction | Fields |
|---|---|---|
| `tcp_open` | server → agent | `id`, `path` (port number) |
| `tcp_ready` | agent → server | `id` |
| `tcp_data` | both | `id`, `data` (base64) |
| `tcp_close` | both | `id` |

`id` is a random UUID generated per connection, used to multiplex multiple TCP streams over a single WebSocket connection.

## Security

- The forward token is a signed JWT (HMAC-SHA256) — cannot be forged without the server's secret.
- Token is tied to a specific workspace ID — cannot be reused across workspaces.
- Password authentication is disabled in sshd — only pubkey.
- The WebSocket forward endpoint is unauthenticated by design (token in query param) to support SSH's `ProxyCommand` which cannot set HTTP headers, but the JWT provides equivalent authentication.
