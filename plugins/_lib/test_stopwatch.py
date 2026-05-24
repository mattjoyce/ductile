"""Tests for the vendored _stopwatch helper.

Uses unittest to match the existing plugin test convention (test_fetch.py).
pytest also discovers unittest.TestCase classes, so these run under either
runner without additional dependencies.
"""

from __future__ import annotations

import time
import unittest

from _stopwatch import _MAX_SUBS, SUBS_RESPONSE_KEY, Spans


class StopwatchSpansTests(unittest.TestCase):
    def test_records_span_with_positive_duration(self) -> None:
        spans = Spans()
        with spans.time("test.work"):
            time.sleep(0.001)

        out = spans.to_response_key()
        self.assertEqual(len(out), 1)
        self.assertEqual(out[0]["name"], "test.work")
        self.assertGreater(out[0]["dur_ns"], 0)

    def test_annotate_adds_fields_to_span(self) -> None:
        spans = Spans()
        with spans.time("test.io") as span:
            span.annotate(bytes=4096, count=1)

        out = spans.to_response_key()
        self.assertEqual(out[0]["bytes"], 4096)
        self.assertEqual(out[0]["count"], 1)

    def test_extra_kwargs_on_time_persist(self) -> None:
        spans = Spans()
        with spans.time("test.tagged", status="ok"):
            pass

        out = spans.to_response_key()
        self.assertEqual(out[0]["status"], "ok")

    def test_exception_in_block_marks_status_err_by_default(self) -> None:
        spans = Spans()
        with self.assertRaises(RuntimeError), spans.time("test.fails"):
            raise RuntimeError("boom")

        out = spans.to_response_key()
        self.assertEqual(out[0]["status"], "err")
        # Span is recorded even though the block raised — caller can still
        # ship sub-spans alongside an error response.
        self.assertEqual(len(out), 1)

    def test_explicit_status_wins_over_exception_default(self) -> None:
        spans = Spans()
        with self.assertRaises(RuntimeError), spans.time("test.timed_out") as span:
            span.annotate(status="timeout")
            raise RuntimeError("ignored")

        out = spans.to_response_key()
        self.assertEqual(out[0]["status"], "timeout")

    def test_add_appends_externally_computed_span(self) -> None:
        spans = Spans()
        spans.add("test.aggregate", dur_ns=12_345, count=42, bytes=1024)

        out = spans.to_response_key()
        self.assertEqual(out[0], {"name": "test.aggregate", "dur_ns": 12345, "count": 42, "bytes": 1024})

    def test_truncates_to_supervisor_cap(self) -> None:
        spans = Spans()
        for i in range(_MAX_SUBS * 2):
            spans.add(f"test.span_{i}", dur_ns=1)

        # __len__ reflects all added spans; to_response_key truncates.
        self.assertEqual(len(spans), _MAX_SUBS * 2)
        out = spans.to_response_key()
        self.assertEqual(len(out), _MAX_SUBS)
        # Head-keep: the first 32 survive (matches supervisor's subs[:32]).
        self.assertEqual(out[0]["name"], "test.span_0")
        self.assertEqual(out[-1]["name"], f"test.span_{_MAX_SUBS - 1}")

    def test_subs_response_key_is_the_documented_constant(self) -> None:
        self.assertEqual(SUBS_RESPONSE_KEY, "ductile_stopwatch_subs")

    def test_dur_ns_is_int_not_float(self) -> None:
        # JSON over the wire prefers ints; perf_counter_ns returns int.
        spans = Spans()
        with spans.time("test.int_check"):
            pass

        out = spans.to_response_key()
        self.assertIsInstance(out[0]["dur_ns"], int)


if __name__ == "__main__":
    unittest.main()
