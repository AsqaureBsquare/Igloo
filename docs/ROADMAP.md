# Igloo product roadmap

Igloo is a self-hosted platform where people and AI agents get isolated workspaces, with a guard layer that controls what a workspace can reach, keeps real credentials out of it, and records what it did.

This roadmap is ordered by what has to be true before the next step is worth doing. Each phase ends with a gate: something observable that says whether to continue, change direction, or stop.

Status key: **done** (built and tested), **next**, **later**, **only if asked for**.

## Where things stand

Done and tested on local k3s and on a real EKS cluster:

- Workspaces from Terraform templates, VS Code and terminal, accounts and SSO, Postgres.
- Egress gateway (`igloo-guard`): host allow-list, private and metadata address blocking, credential routes that inject secrets the workspace never holds, audit events, live policy changes.
- NetworkPolicy confinement, with a `helm test` that proves a cluster enforces it.
- Sandbox API: create, run commands, read and write files, auto-expiry; guarded by default with an empty policy.
- Python client, a narrated demo, and a Network tab in the web UI.
- Supply-chain pinning (Terraform, code-server), private registry support, mirror-only providers.

## Positioning

- **Who it is for:** organisations that cannot run code, secrets or traffic in someone else's cloud (banks, healthcare, regulated engineering), and that want agents working inside their own environment.
- **What is different:** a self-hosted workspace that humans and agents share, with egress control, credential brokering and an audit record built in. Coder lacks the guard layer; E2B and Daytona are mainly aimed at cloud use.
- **What it is not trying to be:** faster than E2B at starting a sandbox, or a general-purpose cloud IDE.

## Phase 0: validate demand (now, in parallel with everything)

Not code. It decides how much of the rest matters.

- Write a one-page pitch and a pilot offer.
- Talk to about ten teams that are blocking or limiting agents today, starting with the author's own organisation.
- Decide the licence (the repository currently contradicts itself between GPL and Apache) and get written confirmation from the employer that the project is acceptable.
- Ask each team: would you run a pilot, what would it have to do, who signs off.

**Gate:** two or three teams agree to a pilot. If none do, stop adding features and change the pitch or the audience.

## Phase 1: a core a security team can trust

Goal: a pilot customer can install it, try to break it, and get a straight answer to "what happens if this is compromised".

- **Baseline policy** (**done**). An admin sets rules users cannot loosen: a deny list that always wins, a ceiling on what may be allowed, no private addresses, always-guarded, and a maximum sandbox lifetime. Checked when a policy is created or changed, and again when the gateway asks, so tightening it reaches existing workspaces within seconds. Admin page: Network baseline; API: `GET /api/guard-baseline`, `PUT /api/admin/guard-baseline`.
- **Close known gaps.**
  - ~~Exec output is visible to anyone with a terminal open on the same workspace~~ (**done**: browsers now receive only terminal messages; replies reach the requester). The agent still keeps one terminal per workspace, which only matters to the owner's second tab and to an admin who opens a user's terminal.
  - ~~No way to change a password~~ (**done**: change your own, admin reset, admin can create a user with a first password; a change ends every older session).
  - ~~DNS is an exfiltration path~~ (**done** for guarded workspaces on the Kubernetes template: no DNS, the server and gateway addresses are in the pod's hosts file). Programs that connect without the proxy cannot resolve names at all, which is the point; anything that must work offline-by-name needs the proxy.
  - Audit table pruning, and a request-size cap on proxied uploads.
- **High availability.** Terminals, tunnels and exec results are relayed in one server process's memory, so only one server replica can run. Move that state to something shared so a pod can die without ending every session.
- **Security evidence.** SBOM, dependency scanning, a written threat model, roles and SSO that cover real use, a documented upgrade path and a vulnerability-reporting process.

**Gate:** a pilot customer completes an install on their own cluster, and a deliberate attack from inside a sandbox (reach a blocked host, read a secret, escape the workspace directory) fails and is logged.

## Phase 2: the features that set it apart

- **Envoy as the data plane.** Keep Igloo's policy, secrets and audit as a Go decision service behind Envoy's external authorization, and put the same live test suite against both the current Go proxy and Envoy before switching the default.
- **Delegated identity.** The gateway obtains a short-lived on-behalf-of token (RFC 8693) from the organisation's identity provider, with the user as subject and the workload as actor, so an internal service can tell a person from an agent acting for them. No user token enters the workspace.
- **Per-path HTTPS policy** through TLS termination, for hosts an organisation wants to allow only in part.
- **A second enforcement layer on AWS:** security groups for pods (or a dedicated node group) so the network guarantee does not depend on the CNI alone.
- **Per-host address ranges** instead of the coarse "allow private addresses" switch, for PrivateLink and similar internal services.

**Gate:** a design partner runs an agent against a real internal service through the gateway, and the service's own logs show the user and the workload.

## Phase 3: make agents want to use it

- **Fast start.** A warm pool of ready sandboxes, then pause and resume at the pod level. Matching E2B's sub-second snapshot start is not a goal.
- **More clients.** A TypeScript client and an MCP server, so common agent frameworks connect without custom code.
- **GPU workspaces.** A GPU template, scale-to-zero nodes with Karpenter, and per-hour usage reports. Request the cloud GPU quota early because approval is slow; build and test the scheduling and metering logic locally against a fake GPU resource, then run one short real test.
- **Autoscaling and a load test** that states how many concurrent sandboxes a given cluster handles.

**Gate:** an agent-framework team integrates it without help from the author.

## Phase 4: commercial options (choose from pilot feedback)

1. **Self-hosted licence and support.** Needs no new product work beyond Phase 1. Start here.
2. **Managed in the customer's own cloud account.** The author operates it; the customer owns the account and the data.
3. **Hosted service.** Needs tenants, quotas, metering, billing, abuse controls, microVM isolation (Firecracker or Kata on KVM-capable machines), a status page and on-call. Only worth building once customers ask for it.

## Not now

- A hosted service before anyone asks to pay for one.
- Snapshots and microVM isolation, which are large and where E2B is far ahead.
- Further UI polish.
- ECS/Fargate support for the guard layer; Fargate has no equivalent enforcement path.

## What can be tested without cloud infrastructure

Locally on k3s: baseline policy, the security fixes, high availability (two server replicas), the Envoy data plane and decision service, delegated identity against a mock identity provider, per-path rules, warm pool, pause and resume, clients, load tests of the gateway and server, and metering with a fake GPU resource.

Needs real cloud: security groups for pods, PrivateLink and Route 53, Karpenter and node scaling, real GPU behaviour and cost, ECR and load-balancer paths, installs on other clusters, and microVM isolation (needs KVM).

## Open decisions

- Licence: GPL or Apache.
- Whether the employer's written confirmation is in place.
- First buyer: the author's own organisation, or a different design partner.
- Whether to open-source the code and sell support, or keep it commercial.
- Whether Dynamo and Mongo backends should implement the guard-event store or be dropped.

## Known limits today

- One server replica only.
- The workspace image is minimal (no Python or Node); a guarded sandbox cannot install packages while starting, so a custom image must already contain what it needs, including `curl`.
- HTTPS to an allowed host is allowed or refused by name, not decrypted.
- An allowed destination can still be misused to send data out; the record shows it, nothing stops it.
- The baseline is one organisation-wide setting; there are no per-organisation or per-team baselines.
- The Dynamo and Mongo backends lack the guard-event store.
