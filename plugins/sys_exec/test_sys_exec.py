"""Tests for the sys_exec plugin.

Spawns the plugin via subprocess and verifies stopwatch sub-span emission
on the success, non-zero-exit, and timeout paths.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
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


def _spans_by_name(resp: dict) -> dict[str, dict]:
    subs = resp.get("ductile_stopwatch_subs", [])
    return {s["name"]: s for s in subs}


class SysExecStopwatchTests(unittest.TestCase):
    def test_success_emits_subprocess_run_and_output_capture(self) -> None:
        resp = run_plugin(
            {
                "command": "handle",
                "config": {"command": ["sh", "-c", "echo hello"]},
                "event": {"payload": {}},
            }
        )
        self.assertEqual(resp["status"], "ok", msg=resp)
        spans = _spans_by_name(resp)
        self.assertIn("sys_exec.subprocess_run", spans)
        self.assertIn("sys_exec.output_capture", spans)

        run = spans["sys_exec.subprocess_run"]
        self.assertEqual(run["status"], "ok")
        self.assertEqual(run["exit_code"], 0)
        self.assertGreater(run["dur_ns"], 0)

        cap = spans["sys_exec.output_capture"]
        # "hello\n" is 6 bytes on stdout, 0 on stderr.
        self.assertEqual(cap["bytes"], 6)

    def test_nonzero_exit_tagged_status_err_with_exit_code(self) -> None:
        resp = run_plugin(
            {
                "command": "handle",
                "config": {"command": ["sh", "-c", "exit 3"]},
                "event": {"payload": {}},
            }
        )
        self.assertEqual(resp["status"], "error", msg=resp)
        spans = _spans_by_name(resp)
        run = spans["sys_exec.subprocess_run"]
        self.assertEqual(run["status"], "err")
        self.assertEqual(run["exit_code"], 3)

    def test_timeout_emits_subprocess_run_status_timeout(self) -> None:
        resp = run_plugin(
            {
                "command": "handle",
                "config": {
                    "command": ["sh", "-c", "sleep 5"],
                    "timeout_seconds": 0.2,
                },
                "event": {"payload": {}},
            }
        )
        self.assertEqual(resp["status"], "error", msg=resp)
        spans = _spans_by_name(resp)
        run = spans["sys_exec.subprocess_run"]
        self.assertEqual(run["status"], "timeout")
        self.assertEqual(run["exit_code"], -1)
        # Should also have emitted output_capture for the (possibly empty) stderr.
        self.assertIn("sys_exec.output_capture", spans)

    def test_output_capture_bytes_reflects_raw_lengths_not_truncated(self) -> None:
        # Emit a known number of bytes on stdout; output_capture.bytes
        # records the raw size BEFORE truncation, so quartile analysis
        # sees real output volume.
        resp = run_plugin(
            {
                "command": "handle",
                "config": {
                    "command": ["sh", "-c", "printf 'x%.0s' $(seq 1 5000)"],
                    "stdout_max_bytes": 64,
                },
                "event": {"payload": {}},
            }
        )
        self.assertEqual(resp["status"], "ok", msg=resp)
        cap = _spans_by_name(resp)["sys_exec.output_capture"]
        self.assertEqual(cap["bytes"], 5000)

    def test_health_emits_no_subs(self) -> None:
        resp = run_plugin(
            {
                "command": "health",
                "config": {"command": ["sh", "-c", "echo x"]},
            }
        )
        self.assertEqual(resp["status"], "ok", msg=resp)
        self.assertNotIn("ductile_stopwatch_subs", resp)


if __name__ == "__main__":
    unittest.main()
