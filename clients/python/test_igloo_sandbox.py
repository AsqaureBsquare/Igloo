import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer

from igloo_sandbox import IglooError, Sandbox


class Stub(BaseHTTPRequestHandler):
    log = []
    statuses = ["creating", "creating", "running"]
    files = {}

    def _send(self, code, body=b"", ctype="application/json"):
        if not isinstance(body, bytes):
            body = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(n)

    def _record(self, method):
        body = self._body()
        Stub.log.append((method, self.path, self.headers.get("Authorization"), body))
        return body

    def do_POST(self):
        body = self._record("POST")
        if self.headers.get("Authorization") != "Bearer tok":
            return self._send(401, {"error": "unauthorized"})
        if self.path == "/api/sandboxes":
            return self._send(201, {"id": "sb1", "status": "creating", "expires_at": "2030-01-01T00:00:00Z"})
        if self.path == "/api/sandboxes/sb1/exec":
            req = json.loads(body)
            if req["cmd"][-1] == "boom":
                return self._send(400, {"error": "no such program"})
            return self._send(200, {"exit_code": 3 if req["cmd"][-1] == "fail" else 0, "stdout": "out:" + req["cmd"][-1], "stderr": "e", "timed_out": False})
        self._send(404, {"error": "nope"})

    def do_GET(self):
        self._record("GET")
        if self.path == "/api/sandboxes/sb1":
            return self._send(200, {"status": Stub.statuses.pop(0) if Stub.statuses else "running"})
        if self.path.startswith("/api/sandboxes/sb1/files"):
            return self._send(200, Stub.files.get("f", b""), "application/octet-stream")
        if self.path.startswith("/api/workspaces/sb1/agent-logs"):
            return self._send(200, b"E: Failed to fetch http://archive.example/InRelease  403  Forbidden\nsh: 16: curl: not found\n", "text/plain")
        if self.path.startswith("/api/workspaces/sb1/activity"):
            return self._send(200, [{"decision": "deny", "host": "x"}])
        self._send(404, {"error": "nope"})

    def do_PUT(self):
        body = self._record("PUT")
        if self.path.startswith("/api/sandboxes/sb1/files"):
            Stub.files["f"] = body
            return self._send(204)
        if self.path.startswith("/api/workspaces/sb1/guard-policy"):
            return self._send(200, {})
        self._send(404, {"error": "nope"})

    def do_DELETE(self):
        self._record("DELETE")
        self._send(204)

    def log_message(self, *a):
        pass


class ClientTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.srv = HTTPServer(("127.0.0.1", 0), Stub)
        threading.Thread(target=cls.srv.serve_forever, daemon=True).start()
        cls.url = f"http://127.0.0.1:{cls.srv.server_port}"

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()

    def setUp(self):
        Stub.log.clear()
        Stub.statuses = ["creating", "creating", "running"]

    def test_create_waits_until_running_and_sends_the_policy(self):
        import igloo_sandbox
        igloo_sandbox.time.sleep = lambda s: None  # don't actually wait between polls
        sb = Sandbox.create(url=self.url, token="tok", allow=["a.example"], ttl_seconds=120, image="img:1")
        self.assertEqual(sb.id, "sb1")
        create = json.loads([l for l in Stub.log if l[0] == "POST"][0][3])
        self.assertEqual(create["guard"]["allow"], ["a.example"])
        self.assertEqual((create["ttl_seconds"], create["image"]), (120, "img:1"))
        self.assertEqual(len([l for l in Stub.log if l[0] == "GET"]), 3)  # polled until running

    def test_default_creation_sends_no_policy_so_the_server_default_of_deny_all_applies(self):
        Stub.statuses = ["running"]
        Sandbox.create(url=self.url, token="tok")
        create = json.loads([l for l in Stub.log if l[0] == "POST"][0][3])
        self.assertNotIn("guard", create)
        self.assertNotIn("unguarded", create)

    def test_unguarded_is_explicit(self):
        Stub.statuses = ["running"]
        Sandbox.create(url=self.url, token="tok", unguarded=True)
        self.assertTrue(json.loads(Stub.log[0][3])["unguarded"])

    def test_exec_string_runs_through_a_shell_and_list_runs_directly(self):
        Stub.statuses = ["running"]
        sb = Sandbox.create(url=self.url, token="tok")
        self.assertEqual(sb.exec("echo hi").stdout, "out:echo hi")
        sent = [json.loads(l[3]) for l in Stub.log if l[1].endswith("/exec")]
        self.assertEqual(sent[0]["cmd"], ["sh", "-c", "echo hi"])
        sb.exec(["python3", "x.py"], cwd="src", stdin="in", timeout=5)
        self.assertEqual(json.loads([l for l in Stub.log if l[1].endswith("/exec")][1][3]),
                         {"cmd": ["python3", "x.py"], "timeout_s": 5, "cwd": "src", "stdin": "in"})

    def test_check_raises_on_failure_and_errors_carry_the_servers_reason(self):
        Stub.statuses = ["running"]
        sb = Sandbox.create(url=self.url, token="tok")
        self.assertEqual(sb.exec("fail").exit_code, 3)
        with self.assertRaises(IglooError) as cm:
            sb.exec("fail", check=True)
        self.assertIn("exit 3", str(cm.exception))
        with self.assertRaises(IglooError) as cm:
            sb.exec(["x", "boom"])
        self.assertEqual((cm.exception.status, cm.exception.message), (400, "no such program"))

    def test_files_round_trip_bytes_and_modes(self):
        Stub.statuses = ["running"]
        sb = Sandbox.create(url=self.url, token="tok")
        sb.write_file("d/x.sh", b"\x00\xffbin", mode=0o755)
        self.assertEqual(sb.read_file("d/x.sh"), b"\x00\xffbin")
        put = [l for l in Stub.log if l[0] == "PUT"][0]
        self.assertIn("mode=0755", put[1])
        self.assertIn("path=d%2Fx.sh", put[1])

    def test_with_block_deletes_even_when_the_body_raises(self):
        Stub.statuses = ["running"]
        with self.assertRaises(RuntimeError):
            with Sandbox.create(url=self.url, token="tok"):
                raise RuntimeError("agent crashed")
        self.assertEqual(Stub.log[-1][0], "DELETE")

    def test_bad_token_and_missing_configuration_give_clear_errors(self):
        with self.assertRaises(IglooError) as cm:
            Sandbox.create(url=self.url, token="wrong")
        self.assertEqual(cm.exception.status, 401)
        with self.assertRaises(IglooError) as cm:
            Sandbox.create(url="", token="")
        self.assertIn("IGLOO_URL", str(cm.exception))
        with self.assertRaises(IglooError) as cm:
            Sandbox.create(url="http://127.0.0.1:1", token="tok")
        self.assertIn("cannot reach", str(cm.exception))

    def test_a_sandbox_that_never_starts_says_why(self):
        from igloo_sandbox import _Http
        import igloo_sandbox
        igloo_sandbox.time.sleep = lambda s: None
        sb = Sandbox(_Http(self.url, "tok"), "sb1")
        Stub.statuses = ["creating"] * 500  # never becomes running
        with self.assertRaises(IglooError) as cm:
            sb.wait_until_running(timeout=0.05)
        msg = str(cm.exception)
        self.assertIn("was still 'creating'", msg)
        self.assertIn("curl: not found", msg)  # the sandbox's own explanation is in the error

    def test_a_sandbox_that_fails_to_start_is_cleaned_up(self):
        Stub.statuses = ["creating", "error"]
        with self.assertRaises(IglooError):
            Sandbox.create(url=self.url, token="tok")
        self.assertEqual(Stub.log[-1][0], "DELETE")


if __name__ == "__main__":
    unittest.main()
