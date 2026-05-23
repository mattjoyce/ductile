#!/usr/bin/env python3
"""folder_watch plugin.

Protocol v2 plugin that periodically scans configured directories and emits
change events when filtered files are created, modified, or deleted.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import sys
import time
from datetime import datetime, timezone
from pathlib import PurePosixPath
from typing import Any, Dict, Iterable, List, Optional, Sequence, Tuple

from _stopwatch import Spans

_DURATION_RE = re.compile(r"^\s*(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)\s*$")
_VALID_STRATEGIES = {"sha256", "mtime_size"}
_VALID_EMIT_MODES = {"aggregate", "per_file"}


def iso_now() -> str:
    return datetime.now(timezone.utc).isoformat()


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


def as_int(value: Any, default: int, minimum: int = 1) -> int:
    if value is None:
        return default
    try:
        parsed = int(value)
    except (TypeError, ValueError):
        return default
    return max(minimum, parsed)


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


def normalize_globs(value: Any, defaults: Sequence[str]) -> List[str]:
    if value is None:
        return list(defaults)
    if isinstance(value, str):
        out = [value.strip()] if value.strip() else []
    elif isinstance(value, list):
        out = [str(v).strip() for v in value if str(v).strip()]
    else:
        out = []
    return out or list(defaults)


def compact_error(message: str, retry: bool = False, logs: Optional[List[Dict[str, str]]] = None) -> Dict[str, Any]:
    return {
        "status": "error",
        "error": message,
        "retry": retry,
        "logs": logs or [{"level": "error", "message": message}],
    }


def compact_ok(
    *,
    result: str,
    events: Optional[List[Dict[str, Any]]] = None,
    state_updates: Optional[Dict[str, Any]] = None,
    logs: Optional[List[Dict[str, str]]] = None,
) -> Dict[str, Any]:
    out: Dict[str, Any] = {"status": "ok", "result": result, "logs": logs or []}
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
        root = str(item.get("root", "")).strip()
        event_type = str(item.get("event_type", "")).strip()
        strategy = str(item.get("strategy", "mtime_size")).strip().lower()
        emit_mode = str(item.get("emit_mode", "aggregate")).strip().lower()

        if not watch_id:
            errors.append(f"{prefix}.id is required")
            continue
        if watch_id in seen_ids:
            errors.append(f"duplicate watch id: {watch_id}")
            continue
        seen_ids.add(watch_id)

        if not root:
            errors.append(f"{prefix}.root is required")
            continue
        if not event_type:
            errors.append(f"{prefix}.event_type is required")
            continue
        if strategy not in _VALID_STRATEGIES:
            errors.append(f"{prefix}.strategy must be one of: {', '.join(sorted(_VALID_STRATEGIES))}")
            continue
        if emit_mode not in _VALID_EMIT_MODES:
            errors.append(f"{prefix}.emit_mode must be one of: {', '.join(sorted(_VALID_EMIT_MODES))}")
            continue

        watches.append(
            {
                "id": watch_id,
                "root": os.path.realpath(os.path.expanduser(root)),
                "event_type": event_type,
                "recursive": as_bool(item.get("recursive"), True),
                "include_globs": normalize_globs(item.get("include_globs"), ["*", "**/*"]),
                "exclude_globs": normalize_globs(item.get("exclude_globs"), []),
                "ignore_hidden": as_bool(item.get("ignore_hidden"), True),
                "emit_mode": emit_mode,
                "emit_initial": as_bool(item.get("emit_initial"), False),
                "min_stable_age": parse_duration_seconds(item.get("min_stable_age"), 0.0),
                "strategy": strategy,
                "max_files": as_int(item.get("max_files"), 5000, minimum=1),
                "max_events": as_int(item.get("max_events"), 500, minimum=1),
            }
        )

    return watches, errors


def is_hidden_path(rel_path: str) -> bool:
    return any(part.startswith(".") for part in rel_path.split("/") if part)


def match_pattern(rel_path: str, pattern: str) -> bool:
    rel = PurePosixPath(rel_path)
    pat = pattern.strip()
    if not pat:
        return False
    if rel.match(pat):
        return True
    if pat.startswith("**/") and rel.match(pat[3:]):
        return True
    return False


def match_any(rel_path: str, patterns: Sequence[str]) -> bool:
    return any(match_pattern(rel_path, pattern) for pattern in patterns)


def iter_candidate_files(root: str, recursive: bool, ignore_hidden: bool) -> Iterable[Tuple[str, str]]:
    if recursive:
        for current_root, dirs, files in os.walk(root, followlinks=False):
            dirs.sort()
            files.sort()
            if ignore_hidden:
                dirs[:] = [d for d in dirs if not d.startswith(".")]
            for filename in files:
                if ignore_hidden and filename.startswith("."):
                    continue
                abs_path = os.path.join(current_root, filename)
                rel_path = os.path.relpath(abs_path, root).replace(os.sep, "/")
                if ignore_hidden and is_hidden_path(rel_path):
                    continue
                yield rel_path, abs_path
        return

    try:
        entries = sorted(os.scandir(root), key=lambda e: e.name)
    except OSError:
        return

    for entry in entries:
        if not entry.is_file(follow_symlinks=False):
            continue
        if ignore_hidden and entry.name.startswith("."):
            continue
        rel_path = entry.name.replace(os.sep, "/")
        if ignore_hidden and is_hidden_path(rel_path):
            continue
        yield rel_path, entry.path


def sha256_file(path: str) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        while True:
            chunk = handle.read(1024 * 1024)
            if not chunk:
                break
            digest.update(chunk)
    return digest.hexdigest()


def fingerprint_file(path: str, strategy: str) -> Tuple[str, int, int]:
    st = os.stat(path)
    mtime_ns = int(getattr(st, "st_mtime_ns", int(st.st_mtime * 1_000_000_000)))
    size = int(st.st_size)
    if strategy == "mtime_size":
        return f"{mtime_ns}:{size}", size, mtime_ns
    return sha256_file(path), size, mtime_ns


def snapshot_hash(files_map: Dict[str, str]) -> str:
    digest = hashlib.sha256()
    for rel in sorted(files_map.keys()):
        digest.update(rel.encode("utf-8"))
        digest.update(b"=")
        digest.update(files_map[rel].encode("utf-8"))
        digest.update(b"\n")
    return digest.hexdigest()


def coerce_file_map(raw: Any) -> Dict[str, str]:
    if not isinstance(raw, dict):
        return {}
    out: Dict[str, str] = {}
    for key, value in raw.items():
        if not isinstance(key, str) or not isinstance(value, str):
            continue
        out[key] = value
    return out


def slice_capped(items: List[str], limit: int) -> Tuple[List[str], bool]:
    if len(items) <= limit:
        return items, False
    return items[:limit], True


def scan_watch(
    watch: Dict[str, Any], now: float
) -> Tuple[
    Dict[str, str],
    Dict[str, Dict[str, int]],
    int,
    int,
    bool,
    List[Dict[str, str]],
    int,
    int,
    int,
]:
    root = watch["root"]
    include_globs = watch["include_globs"]
    exclude_globs = watch["exclude_globs"]
    min_stable_age = float(watch["min_stable_age"])
    max_files = int(watch["max_files"])

    files_map: Dict[str, str] = {}
    meta_map: Dict[str, Dict[str, int]] = {}
    skipped_unstable = 0
    scanned_files = 0
    truncated = False
    logs: List[Dict[str, str]] = []
    fp_count = 0
    fp_bytes = 0
    fp_dur_ns = 0

    for rel_path, abs_path in iter_candidate_files(root, bool(watch["recursive"]), bool(watch["ignore_hidden"])):
        scanned_files += 1

        if include_globs and not match_any(rel_path, include_globs):
            continue
        if exclude_globs and match_any(rel_path, exclude_globs):
            continue

        try:
            st = os.stat(abs_path)
        except OSError as exc:
            logs.append({"level": "warn", "message": f"{watch['id']}: stat failed for {abs_path}: {exc}"})
            continue

        age_seconds = now - float(st.st_mtime)
        if min_stable_age > 0 and age_seconds < min_stable_age:
            skipped_unstable += 1
            continue

        fp_t0 = time.perf_counter_ns()
        try:
            fingerprint, size, mtime_ns = fingerprint_file(abs_path, str(watch["strategy"]))
        except OSError as exc:
            logs.append({"level": "warn", "message": f"{watch['id']}: fingerprint failed for {abs_path}: {exc}"})
            continue
        fp_dur_ns += time.perf_counter_ns() - fp_t0
        fp_count += 1
        fp_bytes += size

        files_map[rel_path] = fingerprint
        meta_map[rel_path] = {"size": size, "mtime_ns": mtime_ns}

        if len(files_map) >= max_files:
            truncated = True
            break

    return files_map, meta_map, scanned_files, skipped_unstable, truncated, logs, fp_count, fp_bytes, fp_dur_ns


def handle_poll(config: Dict[str, Any], state: Dict[str, Any], spans: Spans) -> Dict[str, Any]:
    watches, errors = normalize_watches(config)
    if errors:
        return compact_error("invalid folder_watch config", retry=False, logs=[{"level": "error", "message": e} for e in errors])

    state_watches_raw = state.get("watches", {})
    state_watches = state_watches_raw if isinstance(state_watches_raw, dict) else {}

    next_state_watches: Dict[str, Any] = {}
    events: List[Dict[str, Any]] = []
    logs: List[Dict[str, str]] = []
    now = time.time()

    # Aggregate timing across all watches in this poll, emitted as a small
    # bounded number of spans rather than one-per-file (which would blow
    # the 32 cap on any reasonably-sized scan).
    scan_dur_total_ns = 0
    scanned_watches = 0
    fp_count_total = 0
    fp_bytes_total = 0
    fp_dur_total_ns = 0
    diff_emit_dur_total_ns = 0
    events_emitted_total = 0

    for watch in watches:
        watch_id = watch["id"]
        root = watch["root"]
        prev_raw = state_watches.get(watch_id)
        prev_entry = prev_raw if isinstance(prev_raw, dict) else {}
        known_previous = isinstance(prev_raw, dict)
        prev_files = coerce_file_map(prev_entry.get("files", {}))

        if not os.path.isdir(root):
            logs.append({"level": "error", "message": f"{watch_id}: root directory not found: {root}"})
            next_state_watches[watch_id] = {
                "root": root,
                "files": prev_files,
                "snapshot_hash": str(prev_entry.get("snapshot_hash", "")),
                "updated_at": iso_now(),
                "error": "root_not_found",
            }
            continue

        scan_t0 = time.perf_counter_ns()
        (
            current_files,
            meta_map,
            scanned_files,
            skipped_unstable,
            truncated,
            scan_logs,
            fp_count,
            fp_bytes,
            fp_dur_ns,
        ) = scan_watch(watch, now)
        scan_dur_total_ns += time.perf_counter_ns() - scan_t0
        scanned_watches += 1
        fp_count_total += fp_count
        fp_bytes_total += fp_bytes
        fp_dur_total_ns += fp_dur_ns
        logs.extend(scan_logs)

        diff_t0 = time.perf_counter_ns()
        current_hash = snapshot_hash(current_files)

        prev_keys = set(prev_files.keys())
        current_keys = set(current_files.keys())

        created = sorted(current_keys - prev_keys)
        deleted = sorted(prev_keys - current_keys)
        modified = sorted(path for path in (current_keys & prev_keys) if current_files[path] != prev_files[path])

        has_changes = bool(created or modified or deleted)
        if not known_previous and not bool(watch["emit_initial"]):
            has_changes = False
            created = []
            modified = []
            deleted = []

        if has_changes:
            if watch["emit_mode"] == "aggregate":
                max_events = int(watch["max_events"])
                created_out, created_capped = slice_capped(created, max_events)
                modified_out, modified_capped = slice_capped(modified, max_events)
                deleted_out, deleted_capped = slice_capped(deleted, max_events)
                capped = created_capped or modified_capped or deleted_capped

                events.append(
                    {
                        "type": watch["event_type"],
                        "payload": {
                            "watch_id": watch_id,
                            "root": root,
                            "created": created_out,
                            "modified": modified_out,
                            "deleted": deleted_out,
                            "changed_count": len(created) + len(modified) + len(deleted),
                            "snapshot_hash": current_hash,
                            "payload_capped": capped,
                            "state_truncated": truncated,
                        },
                        "dedupe_key": f"folder_watch:{watch_id}:{current_hash}",
                    }
                )
            else:
                max_events = int(watch["max_events"])
                emitted = 0

                for rel in created:
                    if emitted >= max_events:
                        break
                    emitted += 1
                    events.append(
                        {
                            "type": watch["event_type"],
                            "payload": {
                                "watch_id": watch_id,
                                "root": root,
                                "path": rel,
                                "change_type": "created",
                                "fingerprint": current_files.get(rel, ""),
                                "size": meta_map.get(rel, {}).get("size", 0),
                                "mtime_ns": meta_map.get(rel, {}).get("mtime_ns", 0),
                            },
                            "dedupe_key": f"folder_watch:{watch_id}:{rel}:{current_files.get(rel, '')}",
                        }
                    )

                for rel in modified:
                    if emitted >= max_events:
                        break
                    emitted += 1
                    events.append(
                        {
                            "type": watch["event_type"],
                            "payload": {
                                "watch_id": watch_id,
                                "root": root,
                                "path": rel,
                                "change_type": "modified",
                                "fingerprint": current_files.get(rel, ""),
                                "size": meta_map.get(rel, {}).get("size", 0),
                                "mtime_ns": meta_map.get(rel, {}).get("mtime_ns", 0),
                            },
                            "dedupe_key": f"folder_watch:{watch_id}:{rel}:{current_files.get(rel, '')}",
                        }
                    )

                for rel in deleted:
                    if emitted >= max_events:
                        break
                    emitted += 1
                    previous_fp = prev_files.get(rel, "")
                    events.append(
                        {
                            "type": watch["event_type"],
                            "payload": {
                                "watch_id": watch_id,
                                "root": root,
                                "path": rel,
                                "change_type": "deleted",
                                "previous_fingerprint": previous_fp,
                            },
                            "dedupe_key": f"folder_watch:{watch_id}:{rel}:deleted:{previous_fp}",
                        }
                    )

            logs.append(
                {
                    "level": "info",
                    "message": f"{watch_id}: changes detected created={len(created)} modified={len(modified)} deleted={len(deleted)}",
                }
            )

        if truncated:
            logs.append(
                {
                    "level": "warn",
                    "message": f"{watch_id}: state truncated at max_files={watch['max_files']}",
                }
            )

        logs.append(
            {
                "level": "info",
                "message": f"{watch_id}: scanned={scanned_files} tracked={len(current_files)} skipped_unstable={skipped_unstable}",
            }
        )

        next_state_watches[watch_id] = {
            "root": root,
            "files": current_files,
            "snapshot_hash": current_hash,
            "file_count": len(current_files),
            "scanned_files": scanned_files,
            "skipped_unstable": skipped_unstable,
            "truncated": truncated,
            "strategy": watch["strategy"],
            "updated_at": iso_now(),
        }
        # Close the diff+emit window for this watch — diff_t0 was set
        # immediately after scan_watch returned.
        diff_emit_dur_total_ns += time.perf_counter_ns() - diff_t0

    logs.append({"level": "info", "message": f"folder_watch poll complete: watches={len(watches)} events={len(events)}"})

    if scanned_watches > 0:
        spans.add(
            "folder_watch.scan",
            dur_ns=scan_dur_total_ns,
            count=scanned_watches,
            status="ok",
        )
    if fp_count_total > 0:
        spans.add(
            "folder_watch.fingerprint_total",
            dur_ns=fp_dur_total_ns,
            count=fp_count_total,
            bytes=fp_bytes_total,
            status="ok",
        )
    if scanned_watches > 0:
        spans.add(
            "folder_watch.diff_and_emit",
            dur_ns=diff_emit_dur_total_ns,
            count=len(events),
            status="ok",
        )

    return compact_ok(
        result=f"folder_watch poll complete: watches={len(watches)} events={len(events)}",
        events=events,
        state_updates=snapshot_state(next_state_watches),
        logs=logs,
    )


def handle_health(config: Dict[str, Any]) -> Dict[str, Any]:
    watches, errors = normalize_watches(config)
    if errors:
        return compact_error("invalid folder_watch config", retry=False, logs=[{"level": "error", "message": e} for e in errors])

    issues: List[str] = []
    logs: List[Dict[str, str]] = []

    for watch in watches:
        root = watch["root"]
        if not os.path.isdir(root):
            issues.append(f"{watch['id']}: root directory not found: {root}")
            continue
        if not os.access(root, os.R_OK):
            issues.append(f"{watch['id']}: root directory is not readable: {root}")
            continue
        logs.append({"level": "info", "message": f"{watch['id']}: health OK ({root})"})

    if issues:
        return compact_error("folder_watch health failed", retry=False, logs=[{"level": "error", "message": i} for i in issues])

    return compact_ok(
        result=f"folder_watch health OK (watches={len(watches)})",
        logs=logs or [{"level": "info", "message": "folder_watch health OK"}],
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
