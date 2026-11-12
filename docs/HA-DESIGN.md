# Running more than one server replica

Status: phases 1 and 4 built. Phase 1: owner lease + relay forwarding for exec/files/HTTP,
behind `IGLOO_INTERNAL_ADDR`; concurrent-start migration deadlock fixed with an advisory
lock. Phase 4: device-login codes in the store, first-admin decided under a store-wide
lock (including the OIDC path), one-job-per-workspace enforced in the claim, 3s dispatch
sweep, build logs persisted and tailed by sequence number, and SIGTERM actually stops the
process. Phases 2, 3, 5 and 6 remain design. A single replica without
`IGLOO_INTERNAL_ADDR` behaves exactly as before.

## Why

One server pod means every restart, deploy or node loss drops every open terminal, port-forward,
VS Code session and in-flight command, and Igloo is down while it comes back. A second replica
fixes the downtime only if each request can be served by either replica.

## What lives only in one server's memory today

| State | Where | What breaks with two replicas | Plan |
|---|---|---|---|
| Which agent is connected, its browsers, and server-side listeners (exec replies, port-forward, HTTP proxy) | `relay.Relay` | An agent connects to replica A; a request that lands on B finds no agent ("agent not connected"). Replies go to the replica that holds the agent, not the one waiting. | **Owner table + replica-to-replica forwarding** (below) |
| WireGuard coordinator: one agent peer and N client peers per workspace | `tailnet.Coordinator` | Client and agent on different replicas never meet; packets are not relayed. | Same owner lookup; the client's connection is forwarded to the owning replica |
| Parked provisioner "acquire" slots | `ProvisionerRegistry` | A job enqueued on B is never pushed to a provisioner parked on A (dispatch has no polling). | Every replica also sweeps for pending jobs every ~2 s; the claim is already atomic (`FOR UPDATE SKIP LOCKED`), so two replicas cannot take one job |
| Live build log lines and subscribers | `LogBuffer` | The provisioner reports to A; the browser watching the log is on B and sees nothing. | Persist lines (`workspace_id, seq, line`, trimmed to the last N); readers tail by `seq`; `LISTEN/NOTIFY` carries only the new `seq` |
| CLI device-login codes | `deviceCodes` map | `igloo login` starts on A and polls B: "invalid code". | Table with expiry; one-time use becomes `DELETE ... RETURNING` |
| "First registered user is admin" lock | `regMu` | Two replicas can each create a first admin. | Decide inside one transaction (advisory lock or `INSERT ... WHERE NOT EXISTS`) |
| Login/password throttling counters | `ratelimit` | The limit is multiplied by the replica count. | Counter table with a time window (acceptable to ship later; it degrades the limit, it does not break anything) |
| Network snapshot per workspace | `NetMapStore` | The Network tab shows nothing when it hits the other replica. | Ask the owner replica (same path as the relay), or store the latest snapshot |
| One job at a time per workspace | `inFlight` in worker and daemon | Two jobs for one workspace can run on two processes. | Make `ClaimNextJob` skip workspaces that already have a running job |
| The in-memory store (`--local`) | `store.Memory` | Each replica would have its own database. | Refuse `replicas > 1` with the in-memory store (chart check and server start-up check) |

Already fine: JWT sessions and tokens (stateless, checked against the database), guard authorize
(reads the database), the agent (reconnects with backoff, and lands on any replica).

## The main decision: how a request reaches the replica that holds the agent

Three options.

1. **Postgres `LISTEN/NOTIFY` for everything.** No new dependency, but a payload is limited to
   8 KB and every message goes through the database. Terminal bytes, file contents and tunnel
   packets would be too heavy.
2. **Redis pub/sub.** Fast and simple, but one more thing every operator must run and secure.
3. **Postgres for ownership, direct pod-to-pod for data (recommended).** Each replica writes
   `agent_owner(workspace_id, replica_id, pod_addr, lease_until)` with a heartbeat. A replica that
   needs a workspace's agent looks up the owner; if it is another replica it opens one internal
   WebSocket to `pod_addr` and the two relays join the streams. No new dependency, and the data path
   is one extra hop only when the request lands on the wrong replica.

Details for option 3:

- Ownership is a lease (about 15 s, renewed every 5 s). A crashed replica's rows expire; the agent has
  already reconnected somewhere else and overwrites the row. Writes use `replica_id` so a stale
  replica cannot take a row back.
- Replica-to-replica traffic needs a headless Service so pods can reach each other, an internal
  listener on its own port, and authentication: a key derived from the existing server secret, and a
  NetworkPolicy that only allows server pods to reach it. It is never exposed through the ingress.
- Output rules stay as they are: browsers still receive only `pty_*` messages, and a forwarded
  stream is treated like a local server-side subscriber. Forwarding must not become a way around that.
- With a single replica the code path is the same one with the owner always being itself, so the
  existing tests keep proving it.

## Failure behaviour to promise (and test)

- Killing a replica: its agents reconnect to the other within the backoff window; open terminals on it
  close and the browser reconnects; commands in flight on it fail with a clear error and are not
  silently lost. They are not resumed. That limit should be written in the docs.
- Killing the replica a request was *forwarded through*: the request fails cleanly and can be retried.
- A rolling deploy: readiness stays correct, and a draining replica stops taking new agents first.

## Phases

1. **Done.** Owner table and lease, relay forwarding for exec/files/HTTP (`Subscribe`/`SendToAgent`),
   tests with two in-process relays — plus a live check with two real server processes on one
   Postgres. Enabled per replica by `IGLOO_INTERNAL_ADDR` (the pod address peers dial, e.g.
   `$(POD_IP):8081` from the downward API); `IGLOO_INTERNAL_PORT` (default 8081) is the internal
   listener, `IGLOO_REPLICA_ID` defaults to the hostname, and peers authenticate with a key derived
   from the shared JWT secret. Terminals already work across replicas (they ride the same relay);
   their dedicated phase-2 concerns are reconnect behaviour and port-forward edge cases.
2. Terminals (browser WebSocket) and port-forward through the owner.
3. Tailnet coordinator through the owner.
4. **Done.** Build logs persist (`provision_logs`, trimmed to the last 500 per workspace); a
   subscriber tails the store by sequence number (700ms poll — no LISTEN/NOTIFY needed), so the
   SSE stream works on any replica. Job dispatch sweep (3s, every replica; the claim is atomic);
   device codes in the store (one-time use is a single `DELETE..RETURNING`); first-user lock
   (`pg_advisory_lock`, also covering the previously unlocked OIDC first-login path);
   per-workspace job exclusion inside `ClaimNextJob` in all four stores. Found and fixed along
   the way: SIGTERM never stopped the server (the listener outlived the cancelled context).
5. Chart: `server.replicas`, headless Service, PodDisruptionBudget, internal NetworkPolicy, refusal with
   the in-memory store, readiness/draining.
6. Live test on two replicas: kill one during a terminal session and a running command; rolling restart
   during an e2e run. The EKS test cluster is the place for this.

## Questions for the owner

- Is "in-flight commands fail cleanly, nothing resumes" acceptable for the first version?
- Is Postgres required for more than one replica (no DynamoDB/Mongo support for the new tables at first)?
  The store has three backends today; doing all three triples the work of every phase.
- Should replica-to-replica traffic be allowed to cross nodes only inside the cluster (recommended), or
  must it work across clusters too (a much larger design)?
