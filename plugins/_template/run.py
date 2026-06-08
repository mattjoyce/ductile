#!/usr/bin/env python3
"""_template: the golden-path ductile plugin (Tier 1 — stdlib, privsep-confined).

COPY THIS DIRECTORY to start a new plugin, rename it, and fill in `handle()`.
It is the blessed default pattern: pure standard library, nothing fetched at
spawn, structured protocol-v2 I/O, and it writes only under its own account
state_dir. It already passes discovery and `test_run.py` as-is.

═══ Confined-plugin runtime contract (privsep) — what you may rely on ═══════
Under enforce the gateway drops this process to a dedicated, unprivileged
account uid and gives you a runtime rooted at your account's OWN 0700
state_dir (see docs/adr/confined-plugin-runtime-contract.md):

  • cwd, $HOME and $XDG_CACHE_HOME ALL == your state_dir — writable, private,
    shared with no other account. Write state with RELATIVE paths (or
    Path.cwd() / Path.home()). That is the entire storage story.
  • Secrets arrive in request["secrets"], never the environment, never argv.
  • /tmp is writable and shared; nothing else on the host is yours to write.

What you must NOT do (each fails closed under enforce):
  • write anywhere outside cwd/state_dir (or /tmp) — sibling account dirs,
    the gateway's state root, and system paths are all walled off;
  • read $HOME dotfiles, /home/<user>, or any ambient host credential;
  • fetch dependencies at spawn — no `uv run --script`, no `pip`/`npm install`.
    Need a third-party library? That is the advanced tier; see README "Tiers".

The operator wires privsep (`run_as`) and secrets (`requires_vault` /
`vault_principal`) in config.yaml — see README. Your code just honours the
contract above.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

# Vendored from plugins/_lib/ — copy the helpers a plugin uses into its own
# dir; never import across plugin dirs (spawn-per-invocation isolation). The
# script's directory is on sys.path even when cwd is the state_dir, so these
# resolve at runtime.
from _coerce import as_int
from _response import emit, error, ok
from _stopwatch import Spans

# cwd == your account's private, writable state_dir under privsep. Resolve it
# once and write every state file relative to here — and nowhere else.
STATE_DIR = Path.cwd()
RECORDS = STATE_DIR / "records.jsonl"

_SPANS = Spans()


def handle(config: dict, event: dict, secrets: dict) -> dict:
    """Do the plugin's one job. Replace this body with yours.

    The stub demonstrates every part of the contract: read the triggering
    event, optionally use a delivered secret, write durable state UNDER cwd,
    and emit a structured event for the pipeline.
    """
    payload = event.get("payload", {}) or {}
    max_records = as_int(config.get("max_records"), 1000, minimum=1)

    # Secrets come from the request, NOT the environment. Use them here; never
    # log, echo, or emit them. With `requires_vault: true` in config, their
    # absence fails the spawn closed before this code ever runs.
    token = secrets.get("api_token")  # e.g. for an outbound call you make here

    record = {"payload": payload, "had_token": token is not None}

    # Writing under cwd (== state_dir) is the whole point of the runtime
    # contract: this append permission-denied under the pre-#109 world and
    # simply works now.
    with _SPANS.time("template.write_state") as span:
        with RECORDS.open("a", encoding="utf-8") as fh:
            fh.write(json.dumps(record) + "\n")
        span.annotate(status="ok")

    _trim(RECORDS, max_records)

    return ok(
        result=f"recorded 1 event (token={'yes' if token else 'no'})",
        events=[{"type": "template.handled", "payload": {"had_token": token is not None}}],
        logs=[{"level": "info", "message": f"recorded event under {RECORDS}"}],
        subs=_SPANS.to_response_key(),
    )


def init(config: dict) -> dict:
    """One-time setup. Provision anything your plugin needs under state_dir."""
    RECORDS.touch(exist_ok=True)
    return ok(
        result="initialized",
        logs=[{"level": "info", "message": f"state_dir ready at {STATE_DIR}"}],
    )


def _trim(path: Path, keep: int) -> None:
    """Keep the demo state file bounded; a real plugin manages its own state."""
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except FileNotFoundError:
        return
    if len(lines) > keep:
        path.write_text("\n".join(lines[-keep:]) + "\n", encoding="utf-8")


def main() -> None:
    request = json.loads(sys.stdin.read())
    command = request.get("command", "handle")
    config = request.get("config", {}) or {}
    event = request.get("event", {}) or {}
    secrets = request.get("secrets", {}) or {}

    if command == "health":
        emit(ok(result="healthy", logs=[{"level": "info", "message": "healthy"}]))
    elif command == "init":
        emit(init(config))
    elif command == "handle":
        emit(handle(config, event, secrets))
    else:
        emit(error(f"unknown command: {command}", retry=False))


if __name__ == "__main__":
    main()
