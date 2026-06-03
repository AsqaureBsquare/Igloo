# Igloo

**Serverless dev workspaces. Pay per active minute, not per month.**

Igloo gives every developer a persistent cloud workspace that auto-sleeps when idle and wakes in ~15 seconds — disk and state intact. Deploy it anywhere: AWS ECS, any Kubernetes cluster, or your own servers.

> **Beta** — core functionality works. APIs may change before v1.0.

---

## Why Igloo

| | Traditional (Coder, EC2) | Igloo |
|---|---|---|
| Idle cost | Full VM price 24/7 | **$0 when sleeping** |
| Wake time | Cold start (1-3 min) | **~15 seconds** |
| Deploy target | Single region | **Global — provisioners anywhere** |
| Self-host complexity | PostgreSQL + server + proxy | **Single binary** |
| Connectivity | VPN / WireGuard | **Plain WebSocket (443)** |

---

## How it works

```
Developer → igloo CLI / browser
               ↓
         igloo server  (single Go binary, stateless)
               ↓
         Provisioner daemons  (dial in from any region)
               ↓
         Workspace (Fargate task / Kubernetes pod)
               ↑
         igloo-agent (WebSocket tunnel, no inbound ports)
```

1. **Create** — provisioner runs Terraform to spin up a workspace container
2. **Use** — terminal and VS Code proxy through igloo-agent over WebSocket
3. **Idle** — workspace auto-sleeps after inactivity; compute stops, disk persists
4. **Wake** — first request reschedules the container; disk remounts in ~15s

Provisioner daemons connect **outbound** to the server — deploy them in any private VPC, any cloud, any region. No inbound firewall rules required.

---

## Quickstart (Kubernetes / Helm)

```bash
helm install igloo oci://ghcr.io/asquarebsquare/igloo \
  --version 0.1.1 \
  --set server.jwtSecret=$(openssl rand -hex 32) \
  --set provisioner.psk=$(openssl rand -hex 32) \
  --set server.serverURL=https://igloo.yourdomain.com \
  --set server.ingress.enabled=true \
  --set server.ingress.className=nginx \
  --set server.ingress.host=igloo.yourdomain.com
```

For local testing (no AWS, in-memory store):

```bash
helm install igloo oci://ghcr.io/asquarebsquare/igloo \
  --version 0.1.1 \
  --set server.jwtSecret=$(openssl rand -hex 32) \
  --set provisioner.psk=$(openssl rand -hex 32) \
  --set server.localMode=true
```

---

## CLI

**macOS (Apple Silicon)**
```bash
curl -sSL https://github.com/AsqaureBsquare/Igloo/releases/latest/download/igloo_0.1.0_darwin_arm64.tar.gz | tar xz
sudo mv igloo /usr/local/bin/
```

**macOS (Intel)**
```bash
curl -sSL https://github.com/AsqaureBsquare/Igloo/releases/latest/download/igloo_0.1.0_darwin_amd64.tar.gz | tar xz
sudo mv igloo /usr/local/bin/
```

**Linux**
```bash
curl -sSL https://github.com/AsqaureBsquare/Igloo/releases/latest/download/igloo_0.1.0_linux_amd64.tar.gz | tar xz
sudo mv igloo /usr/local/bin/
```

**Login and SSH into a workspace**
```bash
igloo login https://igloo.yourdomain.com you@example.com

# Add to ~/.ssh/config:
# Host igloo-*
#   ProxyCommand igloo proxy %h
#   User root
#   StrictHostKeyChecking no

ssh igloo-<workspace-id>
```

---

## Configuration

### Server (`igloo server`)

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `IGLOO_JWT_SECRET` | — | 32-byte hex secret (required) |
| `IGLOO_PROVISIONER_PSK` | — | Pre-shared key for provisioner daemons |
| `AWS_REGION` | `us-east-1` | DynamoDB region |
| `IGLOO_DYNAMO_*_TABLE` | — | DynamoDB table names |
| `IGLOO_OIDC_ISSUER_URL` | — | Enable OIDC SSO (any compliant IdP) |
| `IGLOO_DISABLE_PASSWORD_AUTH` | `false` | Force SSO-only login |

Run with `--local` to use an in-memory store (no AWS required).

### Provisioner daemon

| Variable | Description |
|---|---|
| `IGLOO_SERVER_URL` | URL of the igloo server |
| `IGLOO_PROVISIONER_PSK` | Must match server PSK |
| `IGLOO_WORKER_CONCURRENCY` | Parallel Terraform jobs (default: 10) |
| `AWS_REGION` | Region for workspace resources |
| `IGLOO_ECS_CLUSTER` | ECS cluster for Fargate workspaces |
| `IGLOO_TF_STATE_BUCKET` | S3 bucket for Terraform state |

---

## Docker images

```bash
docker pull ghcr.io/asquarebsquare/igloo-server:latest
docker pull ghcr.io/asquarebsquare/igloo-provisioner:latest
```

Images are built for `linux/amd64` and `linux/arm64`.

---

## Architecture details

### Workspace sleep/wake

On **Kubernetes**: scale deployment to 0 replicas (pod deleted, PVC survives).  
On **AWS ECS**: stop Fargate task (EBS volume persists, `deleteOnTermination: false`).

Wake = reschedule pod/task + remount existing disk. No Terraform re-run. ~15s.

### Provisioner dispatch

Provisioner daemons hold open a WebSocket to the server and send "acquire" tokens (one per free worker slot). When a job is enqueued, the server instantly pops a waiting token and dispatches — **sub-millisecond latency**, zero idle polling.

### WebSocket relay

```
Browser ──WSS──► igloo server ◄──WSS── igloo-agent (inside workspace)
```

Both browser and agent connect **outbound** to the server on port 443. No VPN, no WireGuard, no open inbound ports on workspaces. Works through any corporate firewall.

---

## Self-hosting on AWS ECS

Infrastructure templates are in `infra/ecs/`. Requires:
- ECS cluster + Fargate
- DynamoDB (workspaces, users, templates, jobs tables)
- ECR for workspace images
- S3 for Terraform state

```bash
cd infra/ecs
terraform init
terraform apply
```

---

## Contributing

```bash
git clone https://github.com/AsqaureBsquare/Igloo
cd Igloo
go run ./cmd/igloo server --local   # starts server with in-memory store
```

---

## License

Apache 2.0
