#!/usr/bin/env python3
"""file_watch plugin.

Protocol v2 plugin that periodically checks configured files and emits events on
create/modify/delete transitions.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

from _stopwatch import Spans

_DURATION_RE = re.compile(r"^\s*(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)\s*$")
_VALID_STRATEGIES = {"sha256", "mtime_size"}

# A per-watch fingerprint slower than this threshold gets its own
# outlier span so a single slow watch is visible without us emitting
# one span per watch (which would blow the 32-entry cap with many
# watches). Aggregates are emitted regardless.
_FINGERPRINT_SLOW_NS = 50_000_000  # 50ms


def utc_now() -> datetime:
    return datetime.now(timezone.utc)


def iso_now() -> str:
    return utc_now().isoformat()


def as_bool(value: Any, default: bool) -> bool:
    if value is None:
        return default
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return value != 0
    if isinstance(value, str):
        lowered = value.strip().lower()
        if lowered in {"1", "true", "yes", "on"}:
            return True
        if lowered in {"0", "false", "no", "off"}:
            return False
    return default


def parse_duration_seconds(value: Any, default: float = 0.0) -> float:
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
    scale = {
        "ns": 1e-9,
        "us": 1e-6,
        "µs": 1e-6,
        "ms": 1e-3,
        "s": 1.0,
        "m": 60.0,
        "h": 3600.0,
    }[unit]
    return max(0.0, amount * scale)


def compact_error(message: str, retry: bool = False, logs: Optional[List[Dict[str, str]]] = None) -> Dict[str, Any]:
    out: Dict[str, Any] = {
        "status": "error",
        "error": message,
        "retry": retry,
        "logs": logs or [{"level": "error", "message": message}],
    }
    return out


def compact_ok(
    *,
    result: str,
    events: Optional[List[Dict[str, Any]]] = None,
    state_updates: Optional[Dict[str, Any]] = None,
    logs: Optional[List[Dict[str, str]]] = None,
) -> Dict[str, Any]:
    out: Dict[str, Any] = {
        "status": "ok",
        "result": result,
        "logs": logs or [],
    }
    if events:
        out["events"] = events
    if state_updates:
        out["state_updates"] = state_updates
    return out


def attach_subs(response: Dict[str, Any], spans: Spans) -> Dict[str, Any]:
    """Attach sub-spans to a response if any were captured."""
    subs = spans.to_response_key()
    if subs:
        response["ductile_stopwatch_subs"] = subs
    return response


def snapshot_state(watches: Dict[str, Any]) -> Dict[str, Any]:
    return {
        "watches": watches,
        "last_poll_at": iso_now(),
    }


def normalize_watches(config: Dict[str, Any]) -> Tuple[List[Dict[str, Any]], List[str]]:
    raw = config.get("watches", [])
    if raw is None:
        raw = []

    if not isinstance(raw, list):
        return [], ["config.watches must be a list"]

    watches: List[Dict[str, Any]] = []
    errors: List[str] = []
    seen_ids: set[str] = set()

    for i, item in enumerate(raw):
        prefix = f"watches[{i}]"
        if not isinstance(item, dict):
            errors.append(f"{prefix} must be an object")
            continue

        watch_id = str(item.get("id", "")).strip()
        path = str(item.get("path", "")).strip()
        event_type = str(item.get("event_type", "")).strip()
        strategy = str(item.get("strategy", "sha256")).strip().lower()

        if not watch_id:
            errors.append(f"{prefix}.id is required")
            continue
        if watch_id in seen_ids:
            errors.append(f"duplicate watch id: {watch_id}")
            continue
        seen_ids.add(watch_id)

        if not path:
            errors.append(f"{prefix}.path is required")
            continue
        if not event_type:
            errors.append(f"{prefix}.event_type is required")
            continue
        if strategy not in _VALID_STRATEGIES:
            errors.append(f"{prefix}.strategy must be one of: {', '.join(sorted(_VALID_STRATEGIES))}")
            continue

        watches.append(
            {
                "id": watch_id,
                "path": os.path.realpath(os.path.expanduser(path)),
                "event_type": event_type,
                "strategy": strategy,
                "emit_initial": as_bool(item.get("emit_initial"), False),
                "emit_deleted": as_bool(item.get("emit_deleted"), True),
                "min_stable_age": parse_duration_seconds(item.get("min_stable_age"), 0.0),
            }
        )

    return watches, errors


def sha256_file(path: str) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        while True:
            chunk = handle.read(1024 * 1024)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def stat_fingerprint(path: str, strategy: str) -> Tuple[str, int, int]:
    st = os.stat(path)
    mtime_ns = int(getattr(st, "st_mtime_ns", int(st.st_mtime * 1_000_000_000)))
    size = int(st.st_size)
    if strategy == "mtime_size":
        return f"{mtime_ns}:{size}", size, mtime_ns
    return sha256_file(path), size, mtime_ns


def coerce_prev_watch(raw: Any) -> Optional[Dict[str, Any]]:
    if not isinstance(raw, dict):
        return None
    out: Dict[str, Any] = {
        "exists": bool(raw.get("exists", False)),
    }
    fingerprint = raw.get("fingerprint")
    if isinstance(fingerprint, str):
        out["fingerprint"] = fingerprint
    size = raw.get("size")
    if isinstance(size, (int, float)):
        out["size"] = int(size)
    mtime_ns = raw.get("mtime_ns")
    if isinstance(mtime_ns, (int, float)):
        out["mtime_ns"] = int(mtime_ns)
    return out


def watch_event(
    *,
    watch: Dict[str, Any],
    change_type: str,
    entry: Dict[str, Any],
    previous: Optional[Dict[str, Any]],
) -> Dict[str, Any]:
    payload: Dict[str, Any] = {
        "watch_id": watch["id"],
        "path": watch["path"],
        "change_type": change_type,
        "changed_at": iso_now(),
    }

    if change_type == "deleted":
        previous_fingerprint = ""
        if previous and isinstance(previous.get("fingerprint"), str):
            previous_fingerprint = str(previous["fingerprint"])
        payload["previous_fingerprint"] = previous_fingerprint
        dedupe_key = f"file_watch:{watch['id']}:deleted:{previous_fingerprint}"
    else:
        payload["fingerprint"] = entry.get("fingerprint", "")
        payload["size"] = entry.get("size", 0)
        payload["mtime_ns"] = entry.get("mtime_ns", 0)
        dedupe_key = f"file_watch:{watch['id']}:{entry.get('fingerprint', '')}"

    return {
        "type": watch["event_type"],
        "payload": payload,
        "dedupe_key": dedupe_key,
    }


def handle_poll(config: Dict[str, Any], state: Dict[str, Any], spans: Spans) -> Dict[str, Any]:
    watches, errors = normalize_watches(config)
    if errors:
        return compact_error("invalid file_watch config", retry=False, logs=[{"level": "error", "message": e} for e in errors])

    now = time.time()
    state_watches_raw = state.get("watches", {})
    state_watches = state_watches_raw if isinstance(state_watches_raw, dict) else {}

    next_state_watches: Dict[str, Any] = {}
    events: List[Dict[str, Any]] = []
    logs: List[Dict[str, str]] = []

    # Per-fingerprint timing accumulators. Emitted as a single aggregate
    # span after the watch loop so the 32-entry cap is preserved even
    # with many watches; individual outliers (>50ms) emit alongside.
    fp_count = 0
    fp_bytes_total = 0
    fp_dur_total_ns = 0

    for watch in watches:
        watch_id = watch["id"]
        path = watch["path"]
        previous_raw = state_watches.get(watch_id)
        previous = coerce_prev_watch(previous_raw)
        known_previous = previous is not None

        exists = os.path.exists(path) and os.path.isfile(path)

        if exists:
            try:
                st = os.stat(path)
            except OSError as exc:
                logs.append({"level": "warn", "message": f"{watch_id}: stat failed for {path}: {exc}"})
                if known_previous:
                    next_state_watches[watch_id] = previous_raw
                continue

            min_stable_age = float(watch["min_stable_age"])
            age_seconds = now - float(st.st_mtime)
            if min_stable_age > 0 and age_seconds < min_stable_age:
                logs.append(
                    {
                        "level": "info",
                        "message": f"{watch_id}: file not stable yet ({age_seconds:.3f}s < {min_stable_age:.3f}s)",
                    }
                )
                if known_previous:
                    next_state_watches[watch_id] = previous_raw
                continue

            fp_t0 = time.perf_counter_ns()
            try:
                fingerprint, size, mtime_ns = stat_fingerprint(path, str(watch["strategy"]))
            except OSError as exc:
                logs.append({"level": "warn", "message": f"{watch_id}: fingerprint failed for {path}: {exc}"})
                if known_previous:
                    next_state_watches[watch_id] = previous_raw
                continue
            fp_dur = time.perf_counter_ns() - fp_t0
            fp_count += 1
            fp_bytes_total += size
            fp_dur_total_ns += fp_dur
            if fp_dur > _FINGERPRINT_SLOW_NS:
                spans.add(
                    f"file_watch.fingerprint_slow.{watch_id}",
                    dur_ns=fp_dur,
                    bytes=size,
                    status="ok",
                )

            current_entry = {
                "exists": True,
                "fingerprint": fingerprint,
                "size": size,
                "mtime_ns": mtime_ns,
                "path": path,
                "strategy": watch["strategy"],
                "updated_at": iso_now(),
            }

            change_type: Optional[str] = None
            prev_exists = bool(previous and previous.get("exists"))
            prev_fingerprint = str(previous.get("fingerprint", "")) if previous else ""

            if not prev_exists:
                if known_previous or bool(watch["emit_initial"]):
                    change_type = "created"
            elif prev_fingerprint != fingerprint:
                change_type = "modified"

            if change_type:
                events.append(watch_event(watch=watch, change_type=change_type, entry=current_entry, previous=previous))
                logs.append({"level": "info", "message": f"{watch_id}: detected {change_type}"})

            next_state_watches[watch_id] = current_entry
            continue

        current_entry = {
            "exists": False,
            "path": path,
            "updated_at": iso_now(),
        }

        prev_exists = bool(previous and previous.get("exists"))
        if prev_exists and bool(watch["emit_deleted"]):
            events.append(watch_event(watch=watch, change_type="deleted", entry=current_entry, previous=previous))
            logs.append({"level": "info", "message": f"{watch_id}: detected deleted"})

        next_state_watches[watch_id] = current_entry

    logs.append({"level": "info", "message": f"file_watch poll complete: watches={len(watches)} events={len(events)}"})

    if fp_count > 0:
        spans.add(
            "file_watch.fingerprint_total",
            dur_ns=fp_dur_total_ns,
            count=fp_count,
            bytes=fp_bytes_total,
            status="ok",
        )

    return compact_ok(
        result=f"file_watch poll complete: watches={len(watches)} events={len(events)}",
        events=events,
        state_updates=snapshot_state(next_state_watches),
        logs=logs,
    )


def handle_health(config: Dict[str, Any]) -> Dict[str, Any]:
    watches, errors = normalize_watches(config)
    if errors:
        return compact_error("invalid file_watch config", retry=False, logs=[{"level": "error", "message": e} for e in errors])

    issues: List[str] = []
    logs: List[Dict[str, str]] = []

    for watch in watches:
        path = watch["path"]
        parent = str(Path(path).parent)
        if not os.path.isdir(parent):
            issues.append(f"{watch['id']}: parent directory does not exist: {parent}")
            continue

        if os.path.exists(path) and os.path.isdir(path):
            issues.append(f"{watch['id']}: path is a directory, expected file: {path}")
            continue

        if os.path.exists(path) and not os.access(path, os.R_OK):
            issues.append(f"{watch['id']}: file exists but is not readable: {path}")
            continue

        logs.append({"level": "info", "message": f"{watch['id']}: health OK ({path})"})

    if issues:
        return compact_error("file_watch health failed", retry=False, logs=[{"level": "error", "message": i} for i in issues])

    return compact_ok(
        result=f"file_watch health OK (watches={len(watches)})",
        logs=logs or [{"level": "info", "message": "file_watch health OK"}],
    )


def main() -> None:
    try:
        request = json.load(sys.stdin)
    except json.JSONDecodeError as exc:
        json.dump(compact_error(f"invalid JSON input: {exc}", retry=False), sys.stdout)
        sys.stdout.write("\n")
        sys.exit(1)

    command = str(request.get("command", "")).strip()
    config = request.get("config", {})
    state = request.get("state", {})

    if not isinstance(config, dict):
        config = {}
    if not isinstance(state, dict):
        state = {}

    spans = Spans()
    if command == "poll":
        response = handle_poll(config, state, spans)
    elif command == "health":
        response = handle_health(config)
    else:
        response = compact_error(
            f"unknown command: {command!r}. Supported commands: poll, health",
            retry=False,
        )

    attach_subs(response, spans)
    json.dump(response, sys.stdout, separators=(",", ":"))
    sys.stdout.write("\n")
    sys.stdout.flush()


if __name__ == "__main__":
    main()
