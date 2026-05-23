import json
import os
import subprocess
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer


PLUGIN_PATH = os.path.join(os.path.dirname(__file__), "run.py")


class _HostileHeaderHandler(BaseHTTPRequestHandler):
    """Returns a normal body but a non-numeric x-markdown-tokens header."""

    def do_GET(self):
        body = b"<html><body>hello</body></html>"
        self.send_response(200)
        self.send_header("Content-Type", "text/html")
        self.send_header("x-markdown-tokens", "not-a-number")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        pass


class _StaticBodyHandler(BaseHTTPRequestHandler):
    """Returns a configurable static body. body_bytes set as a class attr."""

    body_bytes = b"<html><body>ok</body></html>"
    content_type = "text/html"

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", self.content_type)
        self.send_header("Content-Length", str(len(self.body_bytes)))
        self.end_headers()
        self.wfile.write(self.body_bytes)

    def log_message(self, *_args):
        pass


class _RedirectToHandler(BaseHTTPRequestHandler):
    """301-redirects every GET to redirect_target."""

    redirect_target = "http://127.0.0.1:1/"

    def do_GET(self):
        self.send_response(301)
        self.send_header("Location", self.redirect_target)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def log_message(self, *_args):
        pass


def run_plugin(request):
    # check=False on purpose: pre-fix the plugin crashes with no protocol
    # response; we assert it instead produces a bounded protocol message.
    result = subprocess.run(
        [sys.executable, PLUGIN_PATH],
        input=json.dumps(request),
        text=True,
        capture_output=True,
        check=False,
    )
    if not result.stdout.strip():
        raise AssertionError(
            f"plugin produced no protocol response (exit={result.returncode}, "
            f"stderr={result.stderr!r})"
        )
    return json.loads(result.stdout)


def _serve(handler_class):
    server = HTTPServer(("127.0.0.1", 0), handler_class)
    port = server.server_address[1]
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, port, thread


def _stop_server(server, thread):
    server.shutdown()
    server.server_close()
    thread.join(timeout=5)


class FetchHostileMarkdownTokensTests(unittest.TestCase):
    """Reproduces C-FRO-10: a non-numeric remote x-markdown-tokens header
    hit int() outside the try that handles ValueError, crashing the plugin
    with no protocol response. A hostile upstream header must yield a
    bounded protocol response."""

    def setUp(self):
        self.server, self.port, self.thread = _serve(_HostileHeaderHandler)

    def tearDown(self):
        _stop_server(self.server, self.thread)

    def test_non_numeric_markdown_tokens_is_bounded_error(self):
        resp = run_plugin(
            {
                "protocol": 2,
                "command": "handle",
                "config": {
                    "output_format": "html",
                    # P2-06: loopback is now blocked by default; explicitly
                    # opt-in via allow_hosts so this test still exercises
                    # the actual fetch path.
                    "allow_hosts": ["127.0.0.1"],
                },
                "event": {"payload": {"url": f"http://127.0.0.1:{self.port}/"}},
            }
        )
        self.assertEqual(resp["status"], "error")
        self.assertIn("error", resp)


class FetchEgressPolicyTests(unittest.TestCase):
    """P2-06: bundled fetch plugin must reject loopback, link-local, private,
    and metadata-host targets by default. Redirects to such targets are
    re-validated. Response bodies are size-capped. allow_hosts is an
    explicit operator opt-in."""

    def test_scheme_other_than_http_https_is_rejected(self):
        resp = run_plugin(
            {
                "command": "handle",
                "event": {"payload": {"url": "file:///etc/passwd"}},
            }
        )
        self.assertEqual(resp["status"], "error")
        self.assertIn("egress rejected", resp["error"])
        self.assertFalse(resp.get("retry", True))

    def test_loopback_ipv4_is_rejected_by_default(self):
        resp = run_plugin(
            {
                "command": "handle",
                "event": {"payload": {"url": "http://127.0.0.1:9999/"}},
            }
        )
        self.assertEqual(resp["status"], "error")
        self.assertIn("loopback", resp["error"])

    def test_loopback_literal_localhost_is_rejected_by_default(self):
        resp = run_plugin(
            {
                "command": "handle",
                "event": {"payload": {"url": "http://localhost:9999/"}},
            }
        )
        self.assertEqual(resp["status"], "error")
        self.assertIn("loopback", resp["error"])

    def test_metadata_host_is_rejected_by_default(self):
        resp = run_plugin(
            {
                "command": "handle",
                "event": {"payload": {"url": "http://169.254.169.254/latest/meta-data/"}},
            }
        )
        self.assertEqual(resp["status"], "error")
        # 169.254/16 is link-local; metadata or link-local label both acceptable
        self.assertTrue(
            "metadata" in resp["error"] or "link-local" in resp["error"],
            f"expected metadata/link-local rejection, got {resp['error']!r}",
        )

    def test_link_local_ipv4_is_rejected_by_default(self):
        resp = run_plugin(
            {
                "command": "handle",
                "event": {"payload": {"url": "http://169.254.42.42/"}},
            }
        )
        self.assertEqual(resp["status"], "error")
        self.assertTrue(
            "link-local" in resp["error"] or "metadata" in resp["error"],
            f"expected link-local rejection, got {resp['error']!r}",
        )

    def test_private_rfc1918_is_rejected_by_default(self):
        for url in (
            "http://10.0.0.1/",
            "http://172.16.0.1/",
            "http://192.168.1.1/",
        ):
            with self.subTest(url=url):
                resp = run_plugin(
                    {
                        "command": "handle",
                        "event": {"payload": {"url": url}},
                    }
                )
                self.assertEqual(resp["status"], "error", msg=f"{url}: {resp}")
                self.assertIn("private", resp["error"])

    def test_deny_hosts_blocks_explicit_hostname(self):
        # allow_hosts grants the IP-blocklist bypass, but deny_hosts is
        # checked first and wins.
        resp = run_plugin(
            {
                "command": "handle",
                "config": {
                    "allow_hosts": ["127.0.0.1"],
                    "deny_hosts": ["127.0.0.1"],
                },
                "event": {"payload": {"url": "http://127.0.0.1/"}},
            }
        )
        self.assertEqual(resp["status"], "error")
        self.assertIn("deny_hosts", resp["error"])

    def test_allow_hosts_required_when_non_empty(self):
        resp = run_plugin(
            {
                "command": "handle",
                "config": {"allow_hosts": ["example.com"]},
                "event": {"payload": {"url": "http://other.example.org/"}},
            }
        )
        self.assertEqual(resp["status"], "error")
        self.assertIn("allow_hosts", resp["error"])


class FetchAllowHostsLoopbackOptInTest(unittest.TestCase):
    """P2-06: explicitly allowed hosts bypass the IP blocklist. This is the
    operator opt-in for internal/loopback targets and the path tests use."""

    def setUp(self):
        self.server, self.port, self.thread = _serve(_StaticBodyHandler)

    def tearDown(self):
        _stop_server(self.server, self.thread)

    def test_allow_hosts_grants_loopback(self):
        resp = run_plugin(
            {
                "command": "handle",
                "config": {"allow_hosts": ["127.0.0.1"]},
                "event": {"payload": {"url": f"http://127.0.0.1:{self.port}/"}},
            }
        )
        self.assertEqual(resp["status"], "ok", msg=resp)


class FetchResponseSizeCapTests(unittest.TestCase):
    """P2-06: response bodies are capped by max_response_bytes (default
    1 MiB). Oversized responses are rejected before being decoded or
    persisted to the event payload."""

    def setUp(self):
        # 4 KiB body is plenty to overflow a 1 KiB cap without straining
        # the in-process HTTPServer.
        large_handler = type(
            "_LargeBodyHandler",
            (_StaticBodyHandler,),
            {"body_bytes": b"x" * 4096},
        )
        self.server, self.port, self.thread = _serve(large_handler)

    def tearDown(self):
        _stop_server(self.server, self.thread)

    def test_response_over_cap_is_rejected(self):
        resp = run_plugin(
            {
                "command": "handle",
                "config": {
                    "allow_hosts": ["127.0.0.1"],
                    "max_response_bytes": 1024,
                },
                "event": {"payload": {"url": f"http://127.0.0.1:{self.port}/"}},
            }
        )
        self.assertEqual(resp["status"], "error", msg=resp)
        self.assertIn("max_response_bytes", resp["error"])

    def test_response_under_cap_passes(self):
        resp = run_plugin(
            {
                "command": "handle",
                "config": {
                    "allow_hosts": ["127.0.0.1"],
                    "max_response_bytes": 1024 * 1024,
                },
                "event": {"payload": {"url": f"http://127.0.0.1:{self.port}/"}},
            }
        )
        self.assertEqual(resp["status"], "ok", msg=resp)


class FetchRedirectRevalidationTests(unittest.TestCase):
    """P2-06: when follow_redirects=true, each redirect hop is re-validated
    against the egress policy. A source URL on an allowed loopback host
    that redirects to a deny-listed host must be rejected, not followed."""

    def setUp(self):
        # Redirect every request from the source to a host the operator
        # has explicitly deny-listed. deny_hosts is checked before
        # allow_hosts, so even though 127.0.0.1 is in both lists, the
        # deny check fires for the redirect target's revalidation.
        _RedirectToHandler.redirect_target = "http://blocked.example.invalid/"
        self.server, self.port, self.thread = _serve(_RedirectToHandler)

    def tearDown(self):
        _stop_server(self.server, self.thread)

    def test_redirect_to_denied_host_is_rejected(self):
        resp = run_plugin(
            {
                "command": "handle",
                "config": {
                    "allow_hosts": ["127.0.0.1", "blocked.example.invalid"],
                    "deny_hosts": ["blocked.example.invalid"],
                    "follow_redirects": "true",
                },
                "event": {"payload": {"url": f"http://127.0.0.1:{self.port}/source"}},
            }
        )
        self.assertEqual(resp["status"], "error", msg=resp)
        self.assertIn("deny_hosts", resp["error"])


class FetchHealthCommandTest(unittest.TestCase):
    """Sanity: the health command does not touch the network and should
    not be affected by egress policy."""

    def test_health_returns_ok(self):
        resp = run_plugin({"command": "health"})
        self.assertEqual(resp["status"], "ok")


if __name__ == "__main__":
    unittest.main()
