"""Tests for the folder_watch plugin.

Spawns the plugin via subprocess against real on-disk directories.
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


class FolderWatchPollTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        # Three files of varying sizes so bytes-totals are meaningful.
        for name, size in (("a.txt", 100), ("b.txt", 250), ("c.txt", 50)):
            with open(os.path.join(self.tmp.name, name), "wb") as f:
                f.write(b"x" * size)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def _request(self, **overrides: object) -> dict:
        watch = {
            "id": "w1",
            "root": self.tmp.name,
            "event_type": "folder.changed",
            "strategy": "sha256",
            "emit_initial": True,
            **overrides,
        }
        return {
            "command": "poll",
            "config": {"watches": [watch]},
            "state": {},
        }

    def test_poll_succeeds_and_emits_aggregate_spans(self) -> None:
        resp = run_plugin(self._request())
        self.assertEqual(resp["status"], "ok", msg=resp)

        subs = resp.get("ductile_stopwatch_subs", [])
        names = [s["name"] for s in subs]
        self.assertIn("folder_watch.scan", names)
        self.assertIn("folder_watch.fingerprint_total", names)
        self.assertIn("folder_watch.diff_and_emit", names)

    def test_fingerprint_total_carries_count_and_bytes(self) -> None:
        resp = run_plugin(self._request())
        fp = next(
            s for s in resp["ductile_stopwatch_subs"]
            if s["name"] == "folder_watch.fingerprint_total"
        )
        self.assertEqual(fp["count"], 3)
        self.assertEqual(fp["bytes"], 100 + 250 + 50)
        self.assertGreater(fp["dur_ns"], 0)

    def test_diff_and_emit_count_reflects_events(self) -> None:
        # emit_initial=True with empty state means all three files count
        # as created, producing one aggregate event (default emit_mode).
        resp = run_plugin(self._request())
        diff_span = next(
            s for s in resp["ductile_stopwatch_subs"]
            if s["name"] == "folder_watch.diff_and_emit"
        )
        self.assertEqual(diff_span["count"], len(resp.get("events", [])))

    def test_no_spans_when_root_missing(self) -> None:
        # Missing root short-circuits before scan_watch — no aggregates
        # should be emitted because nothing happened.
        req = self._request()
        req["config"]["watches"][0]["root"] = os.path.join(self.tmp.name, "does_not_exist")
        resp = run_plugin(req)
        self.assertEqual(resp["status"], "ok", msg=resp)
        self.assertNotIn("ductile_stopwatch_subs", resp)

    def test_health_emits_no_subs(self) -> None:
        resp = run_plugin(
            {
                "command": "health",
                "config": {
                    "watches": [
                        {
                            "id": "w1",
                            "root": self.tmp.name,
                            "event_type": "folder.changed",
                            "strategy": "sha256",
                        }
                    ]
                },
            }
        )
        self.assertEqual(resp["status"], "ok", msg=resp)
        self.assertNotIn("ductile_stopwatch_subs", resp)


if __name__ == "__main__":
    unittest.main()
