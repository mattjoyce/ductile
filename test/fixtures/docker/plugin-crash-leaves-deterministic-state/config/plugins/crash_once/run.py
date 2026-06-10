#!/usr/bin/env python3
"""crash_once plugin (protocol v2) — fixture-only.

On `handle` it writes + fsyncs a started-marker (proof the crash happens MID-job,
after spawn and payload delivery), then SIGKILLs its own process: no stdout
response, no clean exit, nothing catchable. `health` answers ok so lock/health
paths work; only real jobs crash.
"""

from __future__ import annotations

import json
import os
import signal
import sys


def main() -> int:
    try:
        req = json.load(sys.stdin)
    except Exception as exc:  # noqa: BLE001 — protocol error reply, then exit
        json.dump({"status": "error", "error": f"invalid request json: {exc}", "retry": False}, sys.stdout)
        sys.stdout.write("\n")
        return 0

    command = str(req.get("command", "")).strip()
    config = req.get("config", {})
    if not isinstance(config, dict):
        config = {}

    if command == "health":
        json.dump({"status": "ok", "result": "crash_once healthy", "logs": []}, sys.stdout)
        sys.stdout.write("\n")
        return 0

    marker = str(config.get("marker_file", "crash-started.marker"))
    with open(marker, "w", encoding="utf-8") as fh:
        fh.write(str(req.get("job_id", "")))
        fh.flush()
        os.fsync(fh.fileno())

    os.kill(os.getpid(), signal.SIGKILL)
    return 0  # unreachable


if __name__ == "__main__":
    raise SystemExit(main())
