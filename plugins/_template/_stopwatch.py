"""Vendored stopwatch helper for ductile Python plugins.

Canonical reference. Copy this file into a plugin directory; do NOT import
across plugin dirs. Spawn-per-invocation isolation is load-bearing — a
shared runtime dep would couple all plugins to every helper edit.

Sub-spans are emitted on the plugin response under the key
``ductile_stopwatch_subs`` (see ``SUBS_RESPONSE_KEY``). The supervisor reads
them, caps at 32 entries (head-keep), and stores verbatim in
``job_stopwatch.subs_json``. See ``docs/PLUGIN_DEVELOPMENT.md`` § Stopwatch
for the convention shape and aggregation patterns.
"""

from __future__ import annotations

import time
from types import TracebackType
from typing import Any

SUBS_RESPONSE_KEY = "ductile_stopwatch_subs"

# Belt-and-braces local cap. Supervisor enforces the same number via
# stopwatch.MaxSubsPerRecord; sending more than this is wasted bytes.
_MAX_SUBS = 32


class Spans:
    """Collector for plugin-emitted sub-spans.

    Usage::

        spans = Spans()
        with spans.time("fetch.http_get") as s:
            with opener.open(req) as resp:
                with spans.time("fetch.body_read") as body:
                    raw = resp.read(limit)
                    body.annotate(bytes=len(raw))

        resp["ductile_stopwatch_subs"] = spans.to_response_key()
    """

    def __init__(self) -> None:
        self._spans: list[dict[str, Any]] = []

    def time(self, name: str, **extra: Any) -> _Span:
        return _Span(self, name, dict(extra))

    def add(self, name: str, dur_ns: int, **extra: Any) -> None:
        """Append a span computed externally (e.g. summing per-item durations).

        Prefer ``time(...)`` for code blocks; use ``add`` for aggregates
        whose duration you tracked manually.
        """
        record: dict[str, Any] = {"name": name, "dur_ns": int(dur_ns)}
        record.update(extra)
        self._spans.append(record)

    def to_response_key(self) -> list[dict[str, Any]]:
        """Return the list to assign to ``ductile_stopwatch_subs``.

        Truncates locally to the supervisor's cap so we never ship bytes
        that will be dropped server-side.
        """
        return self._spans[:_MAX_SUBS]

    def __len__(self) -> int:
        return len(self._spans)


class _Span:
    """Context manager returned by ``Spans.time(...)``."""

    __slots__ = ("_parent", "_name", "_extra", "_t0")

    def __init__(self, parent: Spans, name: str, extra: dict[str, Any]) -> None:
        self._parent = parent
        self._name = name
        self._extra = extra
        self._t0 = 0

    def __enter__(self) -> _Span:
        self._t0 = time.perf_counter_ns()
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> None:
        dur = time.perf_counter_ns() - self._t0
        # Default status to "err" if the block raised; callers can override
        # before exit via annotate(status=...). Allows the natural pattern
        # of letting exceptions bubble while still tagging the span.
        if exc is not None and "status" not in self._extra:
            self._extra["status"] = "err"
        self._parent.add(self._name, dur, **self._extra)

    def annotate(self, **kv: Any) -> None:
        """Add fields to this span before it closes.

        Common keys per the convention: ``status`` (ok|err|skip), ``bytes``
        (for I/O spans), ``count`` (for batch spans).
        """
        self._extra.update(kv)
