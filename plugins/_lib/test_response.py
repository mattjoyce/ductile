"""Tests for the vendored _response helper."""

from __future__ import annotations

import io
import json
import unittest
from unittest.mock import patch

from _response import STATUS_ERROR, STATUS_OK, emit, error, ok


class ResponseBuildersTests(unittest.TestCase):
    def test_ok_minimal(self) -> None:
        r = ok(result="done")
        self.assertEqual(r["status"], STATUS_OK)
        self.assertEqual(r["result"], "done")
        self.assertEqual(r["logs"], [])
        self.assertNotIn("events", r)
        self.assertNotIn("state_updates", r)
        self.assertNotIn("ductile_stopwatch_subs", r)

    def test_ok_with_optional_fields(self) -> None:
        r = ok(
            result="done",
            events=[{"type": "x.done", "payload": {"k": 1}}],
            logs=[{"level": "info", "message": "ok"}],
            state_updates={"cursor": "abc"},
            subs=[{"name": "phase", "dur_ns": 5}],
        )
        self.assertEqual(r["events"][0]["type"], "x.done")
        self.assertEqual(r["state_updates"]["cursor"], "abc")
        self.assertEqual(r["ductile_stopwatch_subs"][0]["name"], "phase")

    def test_ok_omits_empty_optional_collections(self) -> None:
        r = ok(result="x", events=[], state_updates={}, subs=[])
        self.assertNotIn("events", r)
        self.assertNotIn("state_updates", r)
        self.assertNotIn("ductile_stopwatch_subs", r)

    def test_error_minimal_synthesizes_log(self) -> None:
        r = error("bad config")
        self.assertEqual(r["status"], STATUS_ERROR)
        self.assertEqual(r["error"], "bad config")
        self.assertEqual(r["retry"], False)
        self.assertEqual(r["logs"], [{"level": "error", "message": "bad config"}])

    def test_error_retry_flag(self) -> None:
        r = error("transient", retry=True)
        self.assertTrue(r["retry"])

    def test_error_with_explicit_logs_does_not_double_up(self) -> None:
        r = error("x", logs=[{"level": "warn", "message": "y"}])
        self.assertEqual(r["logs"], [{"level": "warn", "message": "y"}])

    def test_error_carries_subs(self) -> None:
        r = error("x", subs=[{"name": "before_fail", "dur_ns": 10}])
        self.assertEqual(r["ductile_stopwatch_subs"][0]["name"], "before_fail")

    def test_emit_writes_compact_json_with_newline(self) -> None:
        buf = io.StringIO()
        with patch("sys.stdout", buf):
            emit(ok(result="done"))
        text = buf.getvalue()
        self.assertTrue(text.endswith("\n"))
        # Compact: no spaces after separators.
        self.assertNotIn(", ", text)
        self.assertNotIn(": ", text)
        # Round-trip parses.
        parsed = json.loads(text)
        self.assertEqual(parsed["result"], "done")


if __name__ == "__main__":
    unittest.main()
