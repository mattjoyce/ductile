"""Tests for the vendored _coerce helper."""

from __future__ import annotations

import unittest

from _coerce import as_bool, as_float, as_int, parse_duration_seconds


class AsBoolTests(unittest.TestCase):
    def test_none_returns_default(self) -> None:
        self.assertTrue(as_bool(None, True))
        self.assertFalse(as_bool(None, False))

    def test_native_bool_passthrough(self) -> None:
        self.assertTrue(as_bool(True, False))
        self.assertFalse(as_bool(False, True))

    def test_numeric(self) -> None:
        self.assertTrue(as_bool(1, False))
        self.assertFalse(as_bool(0, True))
        self.assertTrue(as_bool(2.5, False))

    def test_string_truthy(self) -> None:
        for s in ("1", "true", "TRUE", "  yes ", "on"):
            self.assertTrue(as_bool(s, False), s)

    def test_string_falsy(self) -> None:
        for s in ("0", "false", "no", "OFF"):
            self.assertFalse(as_bool(s, True), s)

    def test_unknown_string_uses_default(self) -> None:
        self.assertTrue(as_bool("maybe", True))
        self.assertFalse(as_bool("maybe", False))

    def test_unknown_type_uses_default(self) -> None:
        self.assertTrue(as_bool([1, 2], True))


class AsIntTests(unittest.TestCase):
    def test_none_returns_default(self) -> None:
        self.assertEqual(as_int(None, 7), 7)

    def test_string_int(self) -> None:
        self.assertEqual(as_int("42", 0), 42)

    def test_unparseable_returns_default(self) -> None:
        self.assertEqual(as_int("nope", 5), 5)

    def test_minimum_clamps(self) -> None:
        self.assertEqual(as_int(0, default=0, minimum=1), 1)
        self.assertEqual(as_int("0", default=0, minimum=1), 1)

    def test_minimum_none_allows_negative(self) -> None:
        self.assertEqual(as_int(-3, 0), -3)


class AsFloatTests(unittest.TestCase):
    def test_none_returns_default(self) -> None:
        self.assertEqual(as_float(None, 1.5), 1.5)

    def test_string_float(self) -> None:
        self.assertAlmostEqual(as_float("3.14", 0.0), 3.14)

    def test_unparseable_returns_default(self) -> None:
        self.assertEqual(as_float("nope", 2.0), 2.0)

    def test_minimum_clamps(self) -> None:
        self.assertEqual(as_float(-0.5, default=0.0, minimum=0.0), 0.0)


class ParseDurationSecondsTests(unittest.TestCase):
    def test_none_returns_default(self) -> None:
        self.assertEqual(parse_duration_seconds(None), 0.0)
        self.assertEqual(parse_duration_seconds(None, default=5.0), 5.0)

    def test_int_and_float_treated_as_seconds(self) -> None:
        self.assertEqual(parse_duration_seconds(5), 5.0)
        self.assertAlmostEqual(parse_duration_seconds(2.5), 2.5)

    def test_negative_numeric_clamps_to_zero(self) -> None:
        self.assertEqual(parse_duration_seconds(-3), 0.0)

    def test_digit_string_is_seconds(self) -> None:
        self.assertEqual(parse_duration_seconds("10"), 10.0)

    def test_duration_suffixes(self) -> None:
        cases: list[tuple[str, float]] = [
            ("500ms", 0.5),
            ("2s", 2.0),
            ("1.5m", 90.0),
            ("2h", 7200.0),
            ("100us", 0.0001),
            ("100µs", 0.0001),
            ("1500ns", 1.5e-6),
        ]
        for text, expected in cases:
            self.assertAlmostEqual(parse_duration_seconds(text), expected, msg=text)

    def test_whitespace_tolerated(self) -> None:
        self.assertEqual(parse_duration_seconds("  500ms "), 0.5)

    def test_garbage_returns_default(self) -> None:
        self.assertEqual(parse_duration_seconds("about 5 seconds", default=1.0), 1.0)
        self.assertEqual(parse_duration_seconds("5 hours", default=1.0), 1.0)
        self.assertEqual(parse_duration_seconds("ms", default=1.0), 1.0)


if __name__ == "__main__":
    unittest.main()
