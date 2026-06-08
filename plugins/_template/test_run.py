"""Tests for the _template golden plugin.

Runs run.py as a subprocess (exactly as the supervisor does), feeding a
protocol-v2 request on stdin, and asserts the runtime contract: state writes
land under the process cwd (== the account state_dir under privsep), a
delivered secret is consumed but NEVER echoed into stdout or state, and the
response envelope is well-formed. Copy + adapt these when you fork the dir.
"""

import json
import subprocess
import sys
from pathlib import Path

PLUGIN_DIR = Path(__file__).resolve().parent
RUN = PLUGIN_DIR / "run.py"


def _invoke(request: dict, cwd: Path):
    """Return (parsed_response, raw_stdout). cwd stands in for the state_dir."""
    proc = subprocess.run(
        [sys.executable, str(RUN)],
        input=json.dumps(request),
        capture_output=True,
        text=True,
        cwd=str(cwd),
    )
    assert proc.returncode == 0, proc.stderr
    return json.loads(proc.stdout), proc.stdout


def test_health(tmp_path):
    resp, _ = _invoke({"command": "health"}, tmp_path)
    assert resp["status"] == "ok"


def test_handle_writes_under_cwd_and_consumes_secret(tmp_path):
    resp, raw = _invoke(
        {
            "command": "handle",
            "event": {"payload": {"hello": "world"}},
            "secrets": {"api_token": "s3cr3t-value"},
        },
        tmp_path,
    )

    assert resp["status"] == "ok"
    # The structured event was emitted and reflects that a secret was present.
    assert resp["events"][0]["type"] == "template.handled"
    assert resp["events"][0]["payload"]["had_token"] is True

    # State landed UNDER the cwd (== state_dir), nowhere else.
    records = tmp_path / "records.jsonl"
    assert records.exists(), "handle() must write its state under cwd/state_dir"
    body = records.read_text()
    assert "world" in body

    # The secret value must never leak — not into stdout, not into state.
    assert "s3cr3t-value" not in raw
    assert "s3cr3t-value" not in body


def test_unknown_command_is_a_clean_error(tmp_path):
    resp, _ = _invoke({"command": "nope"}, tmp_path)
    assert resp["status"] == "error"
    assert resp["retry"] is False
