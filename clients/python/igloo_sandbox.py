"""Run code in an isolated, self-hosted Igloo sandbox. Standard library only.

    from igloo_sandbox import Sandbox

    with Sandbox.create(allow=["api.example.com"]) as sb:
        sb.write_file("hello.sh", "echo hi from the sandbox")
        result = sb.exec(["sh", "hello.sh"])
        print(result.stdout)

By default a sandbox can reach nothing on the network: list the hosts it may use in `allow`.
The default image is minimal (sh, awk, curl); pass image= for one with the runtimes you need.
It is deleted when the `with` block ends, or after `ttl_seconds` if your program dies first.

Configuration: IGLOO_URL (e.g. http://localhost:8080) and IGLOO_TOKEN (an API token).
"""
from __future__ import annotations

import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any, Dict, List, Optional, Sequence, Union

__all__ = ["Sandbox", "ExecResult", "IglooError"]


class IglooError(Exception):
    """The server refused or could not do what was asked."""

    def __init__(self, status: int, message: str):
        super().__init__(f"{message} (HTTP {status})" if status else message)
        self.status = status
        self.message = message


@dataclass
class ExecResult:
    exit_code: int
    stdout: str
    stderr: str
    timed_out: bool = False
    truncated: bool = False  # output beyond 1 MiB per stream was cut off

    @property
    def ok(self) -> bool:
        return self.exit_code == 0 and not self.timed_out


class _Http:
    def __init__(self, base: str, token: str, timeout: float = 120.0):
        self.base = base.rstrip("/")
        self.token = token
        self.timeout = timeout

    def request(self, method: str, path: str, body: Any = None, raw: bool = False, params: Optional[Dict[str, str]] = None):
        url = self.base + path
        if params:
            url += "?" + urllib.parse.urlencode(params)
        data, headers = None, {"Authorization": "Bearer " + self.token}
        if isinstance(body, (bytes, bytearray)):
            data, headers["Content-Type"] = bytes(body), "application/octet-stream"
        elif body is not None:
            data, headers["Content-Type"] = json.dumps(body).encode(), "application/json"
        req = urllib.request.Request(url, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                payload = resp.read()
        except urllib.error.HTTPError as e:
            msg = e.read().decode(errors="replace")
            try:
                msg = json.loads(msg).get("error", msg)
            except ValueError:
                pass
            raise IglooError(e.code, msg.strip() or e.reason) from None
        except urllib.error.URLError as e:
            raise IglooError(0, f"cannot reach {self.base}: {e.reason}") from None
        if raw:
            return payload
        return json.loads(payload) if payload else None


class Sandbox:
    def __init__(self, http: _Http, sandbox_id: str, expires_at: Optional[str] = None):
        self._http = http
        self.id = sandbox_id
        self.expires_at = expires_at

    # ── lifecycle ────────────────────────────────────────────────────────────
    @classmethod
    def create(
        cls,
        *,
        url: Optional[str] = None,
        token: Optional[str] = None,
        name: Optional[str] = None,
        ttl_seconds: int = 3600,
        image: Optional[str] = None,
        allow: Optional[Sequence[str]] = None,
        routes: Optional[Sequence[Dict[str, str]]] = None,
        allow_private: bool = False,
        unguarded: bool = False,
        wait: bool = True,
        start_timeout: float = 300.0,
    ) -> "Sandbox":
        """Create a sandbox. `allow` lists reachable hosts ("api.example.com", "*.example.org");
        `routes` expose an API with a credential the gateway adds, as
        {"name": "llm", "upstream": "https://api.example.com", "header": "Authorization",
         "prefix": "Bearer ", "secret": "NAME_OF_A_GATEWAY_SECRET"}.
        With neither, the sandbox can reach nothing. `unguarded=True` runs without the egress
        gateway (unrestricted network)."""
        url = url or os.environ.get("IGLOO_URL", "")
        token = token or os.environ.get("IGLOO_TOKEN", "")
        if not url or not token:
            raise IglooError(0, "set IGLOO_URL and IGLOO_TOKEN, or pass url= and token=")
        http = _Http(url, token)
        body: Dict[str, Any] = {"ttl_seconds": ttl_seconds}
        if name:
            body["name"] = name
        if image:
            body["image"] = image
        if unguarded:
            body["unguarded"] = True
        elif allow is not None or routes is not None or allow_private:
            body["guard"] = {"allow": list(allow or []), "routes": list(routes or []), "allow_private": allow_private}
        made = http.request("POST", "/api/sandboxes", body)
        sb = cls(http, made["id"], made.get("expires_at"))
        if wait:
            try:
                sb.wait_until_running(start_timeout)
            except BaseException:
                sb.delete(quiet=True)
                raise
        return sb

    def wait_until_running(self, timeout: float = 300.0) -> None:
        deadline = time.monotonic() + timeout
        status = ""
        while time.monotonic() < deadline:
            status = self._http.request("GET", f"/api/sandboxes/{self.id}").get("status", "")
            if status == "running":
                return
            if status in ("error", "deleting", "deleted"):
                raise IglooError(0, f"sandbox {self.id} went to '{status}' while starting{self._why_not_started()}")
            time.sleep(1.0)
        raise IglooError(0, f"sandbox {self.id} was still '{status}' after {timeout:.0f}s{self._why_not_started()}")

    def _why_not_started(self) -> str:
        """The last lines the sandbox itself printed, to explain a start that did not happen
        (for example an image without curl, which a sandbox cannot install while it has no network)."""
        try:
            raw = self._http.request("GET", f"/api/workspaces/{self.id}/agent-logs", raw=True)
            lines = [l.rstrip() for l in raw.decode(errors="replace").splitlines() if l.strip()][-6:]
        except Exception:
            return ""
        return ("\n  last lines from the sandbox:\n    " + "\n    ".join(lines)) if lines else ""

    def delete(self, quiet: bool = False) -> None:
        try:
            self._http.request("DELETE", f"/api/sandboxes/{self.id}")
        except IglooError:
            if not quiet:
                raise

    def __enter__(self) -> "Sandbox":
        return self

    def __exit__(self, *exc) -> None:
        self.delete(quiet=True)

    # ── running things ───────────────────────────────────────────────────────
    def exec(
        self,
        cmd: Union[str, Sequence[str]],
        *,
        cwd: Optional[str] = None,
        timeout: int = 60,
        stdin: Optional[str] = None,
        check: bool = False,
    ) -> ExecResult:
        """Run a command. A string runs through `sh -c`; a list runs the program directly."""
        argv = ["sh", "-c", cmd] if isinstance(cmd, str) else list(cmd)
        body: Dict[str, Any] = {"cmd": argv, "timeout_s": timeout}
        if cwd:
            body["cwd"] = cwd
        if stdin is not None:
            body["stdin"] = stdin
        r = self._http.request("POST", f"/api/sandboxes/{self.id}/exec", body)
        res = ExecResult(r["exit_code"], r["stdout"], r["stderr"], r.get("timed_out", False), r.get("truncated", False))
        if check and not res.ok:
            raise IglooError(0, f"command failed (exit {res.exit_code}{', timed out' if res.timed_out else ''}): {res.stderr.strip() or res.stdout.strip()}")
        return res

    def write_file(self, path: str, data: Union[str, bytes], mode: Optional[int] = None) -> None:
        params = {"path": path}
        if mode is not None:
            params["mode"] = format(mode, "04o")
        raw = data.encode() if isinstance(data, str) else bytes(data)
        self._http.request("PUT", f"/api/sandboxes/{self.id}/files", raw, params=params)

    def read_file(self, path: str) -> bytes:
        return self._http.request("GET", f"/api/sandboxes/{self.id}/files", raw=True, params={"path": path})

    # ── network policy and what happened ─────────────────────────────────────
    def activity(self, limit: int = 100) -> List[Dict[str, Any]]:
        """Network decisions the gateway made for this sandbox, newest first."""
        return self._http.request("GET", f"/api/workspaces/{self.id}/activity", params={"limit": str(limit)})

    def set_policy(self, *, allow: Sequence[str] = (), routes: Sequence[Dict[str, str]] = (), allow_private: bool = False) -> None:
        """Change what the sandbox may reach; takes effect within about 15 seconds."""
        self._http.request("PUT", f"/api/workspaces/{self.id}/guard-policy",
                           {"allow": list(allow), "routes": list(routes), "allow_private": allow_private})
