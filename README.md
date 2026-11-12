# Igloo

**Self-hosted workspaces for AI agents and humans — network-guarded, credential-brokered, audited.**

Igloo runs code you don't fully trust — an AI agent, a job from a queue, a contractor's build — in isolated workspaces on your own Kubernetes cluster. A guarded workspace can only reach the hosts its policy allows, through an egress gateway that injects credentials the workspace never holds and records every connection it allowed or refused. The same platform gives your developers persistent cloud workspaces that sleep when idle and wake in ~15 seconds.

> **Beta** — core functionality works and is tested end to end. APIs may change before v1.0.

---

## Why Igloo

Teams adopting AI agents hit the same two questions: *what can this thing reach?* and *what did it do?* Igloo answers both on your own infrastructure:

- **Egress control by default.** A sandbox starts with a policy that allows nothing. You list the hosts it may reach; the network enforces it (Kubernetes NetworkPolicy — bypassing the proxy gets a closed network, and guarded workspaces have no DNS of their own).
- **Credentials the workload never sees.** Declare a route (`llm → api.example.com, header Authorization, secret LLM_KEY`); the workspace calls the gateway with a scoped token and the gateway injects the real key. Keys never enter the container, its env, or your database.
- **A record of everything.** Every allowed and refused connection is an audit event you can read per workspace, in the UI or the API.
- **Self-hosted, one Helm install.** Single Go binary, bundled Postgres or your own, no SaaS, no per-seat pricing. Your cluster, your data, your policies.
- **Stays up through deploys.** Set `server.replicas: 2` and the control plane survives pod loss and rolling upgrades: replicas share state through Postgres and forward to whichever one holds a workspace's agent — failover in seconds, included in the product, not behind an enterprise license.
- **Humans too.** The same workspaces serve developers: VS Code and terminal in the browser, SSH, auto-sleep when idle (~$0 compute), ~15s wake with disk intact.

How this compares: cloud sandbox services (E2B, Daytona, Modal) run your agents on *their* infrastructure; dev-workspace platforms (Coder) manage workspaces but have no egress gateway, credential broker, or network audit; LLM proxies broker credentials but don't isolate the workload. Igloo packages all of it, self-hosted.

---

## Sandboxes in 30 seconds

```python
from igloo_sandbox import Sandbox   # clients/python — stdlib only; IGLOO_URL and IGLOO_TOKEN in env

with Sandbox.create(allow=["api.example.com"]) as sb:
    sb.write_file("job.sh", "echo working")
    print(sb.exec(["sh", "job.sh"]).stdout)
    print(sb.activity())            # what the gateway allowed and blocked
```

`POST /api/sandboxes` creates a workspace for code: run commands, read and write files, and it deletes itself when its time is up. With no options it sits behind the gateway with a policy that allows **nothing**. `python3 examples/agent_demo.py` walks the whole flow.

A policy with a credential route:

```json
{"allow": ["api.example.com", "*.pypi.org"],
 "routes": [{"name": "llm", "upstream": "https://api.example.com", "header": "Authorization",
             "prefix": "Bearer ", "secret": "LLM_KEY"}]}
```

The workspace calls `$IGLOO_ROUTE_BASE/llm/...` presenting `$IGLOO_ROUTE_TOKEN`; the gateway swaps in the real `LLM_KEY`. Policies change without a restart (~15 seconds). The **Network** tab in the web UI shows each guarded workspace's activity live.

---

## How it works

```
Agent / developer → igloo CLI, browser, or sandbox API
                       ↓
                 igloo server  (single Go binary)
                       ↓
                 Provisioner daemons  (dial in from any region)
                       ↓
          Workspace pod ──allowed hosts only──► igloo-guard (egress gateway)
                 ↑                                   │ injects credentials,
          igloo-agent (outbound WebSocket,           │ records every decision
          no inbound ports)                          ▼
                                               the internet
```

1. **Create** — a provisioner runs Terraform to start the workspace container
2. **Confine** — a NetworkPolicy lets a guarded pod talk only to the Igloo server and the gateway; it has no DNS and no service-account token
3. **Use** — terminal, VS Code, and the sandbox API travel over outbound WebSockets (port 443, no VPN, no inbound rules)
4. **Sleep / wake** — idle workspaces scale to zero, disk persists, first request wakes them in ~15s

---

## Quickstart (Kubernetes / Helm)

The gateway needs a cluster that enforces NetworkPolicy (Cilium, Calico, GKE Dataplane V2, k3s, or the policy agents on EKS/AKS). `helm test igloo` proves yours does — run it.

```bash
git clone https://github.com/AsqaureBsquare/Igloo
cd Igloo

helm install igloo charts/igloo \
  --set server.jwtSecret=$(openssl rand -hex 32) \
  --set provisioner.key=$(openssl rand -hex 32) \
  --set guard.enabled=true \
  --set server.serverURL=https://igloo.yourdomain.com \
  --set server.ingress.enabled=true \
  --set server.ingress.className=nginx \
  --set server.ingress.host=igloo.yourdomain.com

helm test igloo   # proves the cluster actually enforces NetworkPolicy
```

Notes:

- **Gateway secrets:** put upstream keys in a Kubernetes Secret and point `guard.existingSecret` at it — values never reach Igloo's database.
- **Sandbox image:** sandboxes cannot install packages at start-up (no network), so give them an image that has what they need: `--set server.sandboxImage=ghcr.io/asqaurebsquare/igloo-workspace:latest`.
- **No default StorageClass** (bare EKS, some bare-metal): set `postgresql.storageClass` and `provisioner.workspace.stateStorageClass`.
- **High availability:** `--set server.replicas=2` keeps Igloo up through deploys and pod loss (needs the PostgreSQL store; the chart refuses otherwise). Replicas forward to each other over an internal port that never touches your ingress. Design and failure semantics: `docs/HA-DESIGN.md`.
- A published OCI chart exists (`oci://ghcr.io/asquarebsquare/igloo`) but currently lags this repository; install from source until the next chart release.

### Organisation-wide baseline

An administrator sets rules every workspace and sandbox policy must stay inside — **Network baseline** in the admin menu, or `PUT /api/admin/guard-baseline`:

```json
{
  "mode": "restricted",                    // or "open": reach anything not denied
  "deny": ["*.pastebin.example"],          // never reachable, wins over any allow
  "ceiling": ["*.corp.example"],           // optional: nothing outside these may be allowed
  "forbid_private": true,                  // no private/internal addresses
  "require_guard": true,                   // every new workspace runs behind the gateway
  "max_sandbox_ttl_seconds": 3600
}
```

A policy that breaks the baseline is refused with a 403 saying why. Tightening the baseline applies to existing workspaces within seconds — the gateway asks the server what a workspace may do, and the answer has the baseline applied (`GET .../guard-policy` shows the `effective` policy). In **open** mode a workspace reaches everything except the deny list; credentials, audit, and private-address blocking still apply.

### What the guard layer does not do

HTTPS to an allowed host is allowed or refused **by name, not decrypted** — there is no per-path policy. An allowed destination can still be misused (data pushed to an allowed code host). Workspaces are container-isolated by default; a workspace or sandbox can request a Kubernetes RuntimeClass (`runtime_class`, e.g. Kata Containers) for microVM isolation on nodes that provide one — and `server.sandboxRuntimeClass` makes that the default for every sandbox. It narrows exposure and records it; it does not make an agent safe.

---

## Developer workspaces

| | Traditional (Coder, EC2) | Igloo |
|---|---|---|
| Idle cost | Full VM price 24/7 | **$0 when sleeping** |
| Wake time | Cold start (1–3 min) | **~15 seconds** |
| Deploy target | Single region | **Provisioners anywhere** |
| Self-host | PostgreSQL + server + proxy | **Single binary** |
| Connectivity | VPN / WireGuard | **Plain WebSocket (443)** |
| High availability | Paid enterprise feature | **`server.replicas: 2`, included** |

Sleep scales the deployment to zero (Kubernetes) or stops the Fargate task (ECS); the disk persists. Wake reschedules and remounts — no Terraform re-run.

### CLI

```bash
# macOS (Apple Silicon) — see Releases for other platforms
curl -sSL https://github.com/AsqaureBsquare/Igloo/releases/latest/download/igloo_0.1.0-beta.1_darwin_arm64.tar.gz | tar xz
sudo mv igloo /usr/local/bin/

igloo login https://igloo.yourdomain.com you@example.com

# ~/.ssh/config:
#   Host igloo-*
#     ProxyCommand igloo proxy %h
#     User root
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
| `IGLOO_OIDC_ISSUER_URL` | — | Enable OIDC SSO (any compliant IdP) |
| `IGLOO_DISABLE_PASSWORD_AUTH` | `false` | Force SSO-only login |
| `AWS_REGION` | `us-east-1` | DynamoDB region (DynamoDB store only) |

Run with `--local` for an in-memory store (no database required). The Helm chart bundles PostgreSQL by default; `server.database.postgresURL` points at your own.

### Provisioner daemon

| Variable | Description |
|---|---|
| `IGLOO_SERVER_URL` | URL of the igloo server |
| `IGLOO_PROVISIONER_PSK` | Must match server PSK |
| `IGLOO_WORKER_CONCURRENCY` | Parallel Terraform jobs (default: 10) |
| `IGLOO_WORKSPACE_NAMESPACE` | Namespace workspace pods run in (default: `workspaces`) |
| `IGLOO_ECS_CLUSTER` | ECS cluster, for Fargate workspaces |
| `IGLOO_TF_STATE_BUCKET` | S3 bucket for Terraform state, or `local` for a PVC |

Provisioner daemons connect **outbound** to the server — deploy them in any private VPC, any cloud, any region. No inbound firewall rules.

---

## Docker images

```bash
docker pull ghcr.io/asquarebsquare/igloo-server:latest
docker pull ghcr.io/asquarebsquare/igloo-provisioner:latest
docker pull ghcr.io/asquarebsquare/igloo-guard:latest       # the egress gateway
docker pull ghcr.io/asquarebsquare/igloo-workspace:latest   # batteries-included workspace/sandbox image
```

Built for `linux/amd64` and `linux/arm64`.

---

## Architecture details

### WebSocket relay

```
Browser ──WSS──► igloo server ◄──WSS── igloo-agent (inside workspace)
```

Browser and agent both connect outbound on 443. No VPN, no WireGuard requirement, no open inbound ports on workspaces. Works through corporate firewalls.

### Provisioner dispatch

Daemons hold a WebSocket open and park "acquire" tokens (one per free worker slot). An enqueued job is pushed to a waiting token immediately — sub-millisecond dispatch, zero idle polling.

### AWS ECS

Terraform for an ECS/Fargate deployment is in `infra/ecs/` (DynamoDB store, EBS-persistent workspaces). Kubernetes is the primary target.

---

## Contributing

```bash
git clone https://github.com/AsqaureBsquare/Igloo
cd Igloo
go run ./cmd/igloo server --local   # in-memory store, no cluster needed
go test ./...
```

---

## License

Apache 2.0
