"""Tests for the file_watch plugin.

Spawns the plugin via subprocess (matches the existing test_fetch.py
pattern) and asserts on protocol responses against real files on disk.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest

PLUGIN_PATH = os.path.join(os.path.dirname(__file__), "run.py")


def run_plugin(request: dict) -> dict:
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


class FileWatchPollTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.path = os.path.join(self.tmp.name, "watched.txt")
        with open(self.path, "wb") as f:
            f.write(b"initial content")

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def _request(self, **overrides: object) -> dict:
        watch = {
            "id": "w1",
            "path": self.path,
            "event_type": "file.changed",
            "strategy": "sha256",
            "emit_initial": True,
            **overrides,
        }
        return {
            "command": "poll",
            "config": {"watches": [watch]},
            "state": {},
        }

    def test_poll_succeeds_on_existing_file(self) -> None:
        resp = run_plugin(self._request())
        self.assertEqual(resp["status"], "ok", msg=resp)

    def test_poll_emits_fingerprint_total_span(self) -> None:
        resp = run_plugin(self._request())
        subs = resp.get("ductile_stopwatch_subs", [])
        names = [s["name"] for s in subs]
        self.assertIn("file_watch.fingerprint_total", names)

        fp_total = next(s for s in subs if s["name"] == "file_watch.fingerprint_total")
        self.assertEqual(fp_total["count"], 1)
        self.assertGreater(fp_total["bytes"], 0)
        self.assertGreater(fp_total["dur_ns"], 0)
        self.assertEqual(fp_total["status"], "ok")

    def test_no_fingerprint_span_when_file_missing(self) -> None:
        # Point at a path that doesn't exist; no fingerprint happens.
        req = self._request()
        req["config"]["watches"][0]["path"] = os.path.join(self.tmp.name, "missing.txt")
        resp = run_plugin(req)
        self.assertEqual(resp["status"], "ok", msg=resp)
        subs = resp.get("ductile_stopwatch_subs", [])
        names = [s["name"] for s in subs]
        self.assertNotIn("file_watch.fingerprint_total", names)

    def test_fingerprint_total_aggregates_across_watches(self) -> None:
        path2 = os.path.join(self.tmp.name, "watched2.txt")
        with open(path2, "wb") as f:
            f.write(b"second file content here")

        req = {
            "command": "poll",
            "config": {
                "watches": [
                    {
                        "id": "w1",
                        "path": self.path,
                        "event_type": "file.changed",
                        "strategy": "sha256",
                        "emit_initial": True,
                    },
                    {
                        "id": "w2",
                        "path": path2,
                        "event_type": "file.changed",
                        "strategy": "sha256",
                        "emit_initial": True,
                    },
                ]
            },
            "state": {},
        }
        resp = run_plugin(req)
        self.assertEqual(resp["status"], "ok", msg=resp)
        fp_total = next(
            s for s in resp["ductile_stopwatch_subs"] if s["name"] == "file_watch.fingerprint_total"
        )
        self.assertEqual(fp_total["count"], 2)
        self.assertGreater(fp_total["bytes"], len(b"initial content"))

    def test_health_succeeds_and_emits_no_subs(self) -> None:
        resp = run_plugin(
            {
                "command": "health",
                "config": {
                    "watches": [
                        {
                            "id": "w1",
                            "path": self.path,
                            "event_type": "file.changed",
                            "strategy": "sha256",
                        }
                    ]
                },
            }
        )
        self.assertEqual(resp["status"], "ok", msg=resp)
        # health does not fingerprint; no spans expected.
        self.assertNotIn("ductile_stopwatch_subs", resp)


if __name__ == "__main__":
    unittest.main()
