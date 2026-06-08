"""Vendored response builders for ductile Python plugins.

Canonical reference for protocol v2 response shape. Copy into plugin dirs;
do not import across plugins (spawn-per-invocation isolation).

The supervisor accepts any JSON conforming to the protocol; these helpers
just remove boilerplate and make the response shape uniform across the
exemplar plugins.
"""

from __future__ import annotations

import json
import sys
from typing import Any

STATUS_OK = "ok"
STATUS_ERROR = "error"

LogEntry = dict[str, str]
Event = dict[str, Any]


def ok(
    *,
    result: str,
    events: list[Event] | None = None,
    logs: list[LogEntry] | None = None,
    state_updates: dict[str, Any] | None = None,
    subs: list[dict[str, Any]] | None = None,
) -> dict[str, Any]:
    """Build a success response. ``subs`` populates ``ductile_stopwatch_subs``."""
    response: dict[str, Any] = {"status": STATUS_OK, "result": result, "logs": logs or []}
    if events:
        response["events"] = events
    if state_updates:
        response["state_updates"] = state_updates
    if subs:
        response["ductile_stopwatch_subs"] = subs
    return response


def error(
    message: str,
    *,
    retry: bool = False,
    events: list[Event] | None = None,
    logs: list[LogEntry] | None = None,
    subs: list[dict[str, Any]] | None = None,
) -> dict[str, Any]:
    """Build an error response."""
    response: dict[str, Any] = {
        "status": STATUS_ERROR,
        "error": message,
        "retry": retry,
        "logs": logs or [{"level": "error", "message": message}],
    }
    if events:
        response["events"] = events
    if subs:
        response["ductile_stopwatch_subs"] = subs
    return response


def emit(response: dict[str, Any]) -> None:
    """Write a response to stdout in the supervisor's expected form.

    Uses compact separators (no whitespace) since the supervisor parses
    JSON, not formats it for humans.
    """
    json.dump(response, sys.stdout, separators=(",", ":"))
    sys.stdout.write("\n")
    sys.stdout.flush()
