"""Vendored value coercion helpers for ductile Python plugins.

Canonical reference. Copy into plugin dirs; do not import across plugins
(spawn-per-invocation isolation).

These helpers consolidate the as_bool / coerce_int / parse_duration_seconds
patterns currently duplicated across file_watch, folder_watch, and sys_exec.
The shape and semantics are deliberately identical to those duplicates so
plugins can vendor this file in place of their inline copies without
behavioural change.
"""

from __future__ import annotations

import re
from typing import Any

_TRUTHY = frozenset({"1", "true", "yes", "on"})
_FALSY = frozenset({"0", "false", "no", "off"})
_DURATION_RE = re.compile(r"^\s*(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)\s*$")
_DURATION_SCALE: dict[str, float] = {
    "ns": 1e-9,
    "us": 1e-6,
    "µs": 1e-6,
    "ms": 1e-3,
    "s": 1.0,
    "m": 60.0,
    "h": 3600.0,
}


def as_bool(value: Any, default: bool) -> bool:
    if value is None:
        return default
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return value != 0
    if isinstance(value, str):
        lowered = value.strip().lower()
        if lowered in _TRUTHY:
            return True
        if lowered in _FALSY:
            return False
    return default


def as_int(value: Any, default: int, minimum: int | None = None) -> int:
    if value is None:
        return default
    try:
        parsed = int(value)
    except (TypeError, ValueError):
        return default
    if minimum is not None and parsed < minimum:
        return minimum
    return parsed


def as_float(value: Any, default: float, minimum: float | None = None) -> float:
    if value is None:
        return default
    try:
        parsed = float(value)
    except (TypeError, ValueError):
        return default
    if minimum is not None and parsed < minimum:
        return minimum
    return parsed


def parse_duration_seconds(value: Any, default: float = 0.0) -> float:
    """Parse a Go-style duration string (e.g. "500ms", "2.5s", "1h") into seconds.

    Bare numbers (int/float/digit-only string) are interpreted as seconds.
    Unparseable strings return ``default``. Negative results are clamped to 0.
    """
    if value is None:
        return default
    if isinstance(value, (int, float)):
        return max(0.0, float(value))
    if not isinstance(value, str):
        return default

    text = value.strip()
    if not text:
        return default
    if text.isdigit():
        return float(text)

    match = _DURATION_RE.match(text)
    if not match:
        return default

    amount = float(match.group(1))
    unit = match.group(2)
    return max(0.0, amount * _DURATION_SCALE[unit])
