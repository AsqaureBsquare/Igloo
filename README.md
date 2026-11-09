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
curl -sSL https://github.com/AsqaureBsquare/Igloo/releases/latest/download/igloo_0.1.0-beta.1_darwin_arm64.tar.gz | tar xz
sudo mv igloo /usr/local/bin/
```

**macOS (Intel)**
```bash
curl -sSL https://github.com/AsqaureBsquare/Igloo/releases/latest/download/igloo_0.1.0-beta.1_darwin_amd64.tar.gz | tar xz
sudo mv igloo /usr/local/bin/
```

**Linux**
```bash
curl -sSL https://github.com/AsqaureBsquare/Igloo/releases/latest/download/igloo_0.1.0-beta.1_linux_amd64.tar.gz | tar xz
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

## Sandboxes and the egress gateway

Igloo can run code, including AI agents, in an isolated workspace whose network access you control,
with a record of everything it did. Two parts:

- **The egress gateway** (`igloo-guard`). A workspace created with a guard policy can only reach the Igloo
  server and the gateway (a Kubernetes network policy enforces this, so ignoring the proxy settings
  gets it nowhere). It has no DNS: the gateway looks up the names it is asked for, and the two
  addresses the workspace needs are written into its hosts file. The gateway allows the hosts you list, refuses everything else, adds
  credentials the workspace never holds, and reports every decision.
- **Sandboxes.** `POST /api/sandboxes` creates a workspace for code: run commands, read and write files,
  and it deletes itself after its lifetime. With no options it is behind the gateway with a policy that
  allows nothing.

Turn the gateway on in the chart (it is off by default). It needs a cluster that enforces NetworkPolicy
(Cilium, Calico, GKE Dataplane V2, or the policy add-on on EKS/AKS); run `helm test <release>` after
installing to prove yours does.

```yaml
guard:
  enabled: true
  existingSecret: my-gateway-secrets   # keys are secret names, e.g. UPSTREAM_KEY; values never reach Igloo's database
server:
  sandboxImage: ghcr.io/asqaurebsquare/igloo-workspace:latest   # sandboxes cannot install anything while starting
```

Use it from Python (standard library only; see `clients/python`):

```python
from igloo_sandbox import Sandbox   # IGLOO_URL and IGLOO_TOKEN in the environment

with Sandbox.create(allow=["api.example.com"]) as sb:
    sb.write_file("job.sh", "echo working")
    print(sb.exec(["sh", "job.sh"]).stdout)
    print(sb.activity())            # what the gateway allowed and blocked
```

`python3 examples/agent_demo.py` walks through it end to end. A policy looks like this:

```json
{"allow": ["api.example.com", "*.pypi.org"],
 "routes": [{"name": "llm", "upstream": "https://api.example.com", "header": "Authorization",
             "prefix": "Bearer ", "secret": "LLM_KEY"}]}
```

A workspace reaches a route at `$IGLOO_ROUTE_BASE/llm/...` presenting `$IGLOO_ROUTE_TOKEN`; the gateway
swaps in the real `LLM_KEY`. Policies change without a restart (about 15 seconds). The Network tab in the
web UI shows each guarded workspace's activity.

What this does not do: HTTPS to an allowed host is allowed or refused by name, not decrypted, so there is
no per-path policy; an allowed destination can still be misused (for example pushing data to an allowed
code host); and there is no organisation-wide baseline policy yet. It narrows exposure and records it; it
does not make an agent safe.

### Organisation-wide baseline

An administrator can set rules that every workspace and sandbox policy must stay inside, from
**Network baseline** in the admin menu or `PUT /api/admin/guard-baseline`:

```json
{
  "mode": "restricted",                    // or "open": every guarded workspace may reach anything not denied
  "deny": ["*.pastebin.example"],          // never reachable, wins over any allow
  "ceiling": ["*.corp.example"],           // optional: nothing outside these may be allowed
  "forbid_private": true,                  // no allow_private
  "require_guard": true,                   // every new workspace runs behind the gateway
  "max_sandbox_ttl_seconds": 3600
}
```

In **open** mode a workspace's own allow-list no longer matters: it can reach everything except the deny
list, and private or internal addresses stay blocked while `forbid_private` is set. Credentials, audit and
the deny list work as before. An open baseline cannot be combined with a ceiling.

A policy that breaks the baseline is refused with a 403 saying why. The baseline is also applied
when the gateway asks what a workspace may do, so tightening it takes effect for existing
workspaces within seconds (the stored policy is not rewritten; `GET .../guard-policy` shows an
`effective` policy next to it). Sandboxes older than a tightened lifetime cap are removed by the
next expiry pass. Workspaces created without the gateway are not moved behind it.

## Docker images

```bash
docker pull ghcr.io/asquarebsquare/igloo-server:latest
docker pull ghcr.io/asquarebsquare/igloo-provisioner:latest
docker pull ghcr.io/asquarebsquare/igloo-guard:latest   # the egress gateway (optional)
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
