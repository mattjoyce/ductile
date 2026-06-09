#!/usr/bin/env bash
# sys_exec — polyglot subprocess round-trip, VAULT-NATIVE. Proves the gateway
# spawns a real (python) subprocess plugin that does work and writes to disk,
# through the live API. The api token is minted from genesis via the credential
# ladder (no literal token):
#   keygen + genesis -> management posture (mint api token) -> config lock +
#   plugin lock -> gateway -> trigger test-pipeline -> subprocess writes output.txt.
set -euo pipefail

ROOT_DIR="${ROOT_DIR:?}"
FIXTURE_NAME="${FIXTURE_NAME:?}"
ARTIFACT_ROOT="${ARTIFACT_ROOT:?}"
# shellcheck source=/dev/null
source "$ROOT_DIR/scripts/test-docker-lib"
# shellcheck source=/dev/null
source "$ROOT_DIR/scripts/test-docker-vault-lib"
fixture_init

BIN="$ROOT_DIR/ductile"
API="http://127.0.0.1:18181"
API_TOKEN_VALUE="sysexec-api-admin-token"
SOCK="/tmp/dtl-sysexec-$$.sock"
SCENARIO_LOG="$ARTIFACT_DIR/scenario.log"
exec > >(tee "$SCENARIO_LOG") 2>&1

WORK="$(mktemp -d)"
WORK="$(cd "$WORK" && pwd -P)"
CONFIG_DIR="$WORK/config"
STATE_DIR="$CONFIG_DIR/state"
PID=""
cp -R "$FIXTURE_DIR/config/." "$CONFIG_DIR/"
chmod +x "$CONFIG_DIR/plugins/sys_exec/run.py" 2>/dev/null || true
mkdir -p "$STATE_DIR"

cleanup() {
  if [[ -n "$PID" ]] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  rm -f "$SOCK"
  fixture_capture_tree "$STATE_DIR" state-dir
  rm -rf "$WORK"
}
trap cleanup EXIT

# --- genesis + bootstrap the api token via the management posture ------------
fixture_log "genesis: keygen + vault init"
fixture_vault_init "$CONFIG_DIR" >/dev/null
fixture_bootstrap_vault "$CONFIG_DIR" "$SOCK" ductile-api-admin "$API_TOKEN_VALUE"

# --- seal + boot the gateway -------------------------------------------------
fixture_log "config lock + plugin lock sys_exec"
"$BIN" config lock --config "$CONFIG_DIR" >"$ARTIFACT_DIR/lock.log" 2>&1
"$BIN" plugin lock sys_exec --config "$CONFIG_DIR" >>"$ARTIFACT_DIR/lock.log" 2>&1

fixture_log "boot gateway"
# Plugin processes inherit ductile's cwd; running from CONFIG_DIR anchors relative
# working_dir entries (e.g. ./state) to the fixture's config tree.
cd "$CONFIG_DIR"
"$BIN" system start --config "$CONFIG_DIR" >"$ARTIFACT_DIR/ductile.log" 2>&1 &
PID=$!
ready=0
for _ in $(seq 1 60); do
  curl -fsS "$API/healthz" >"$ARTIFACT_DIR/healthz.json" 2>/dev/null && { ready=1; break; }
  sleep 0.25
done
[[ "$ready" -eq 1 ]] || fixture_fail "health endpoint did not become ready"
posture="$(jq -r '.posture // empty' "$ARTIFACT_DIR/healthz.json")"
[[ "$posture" == "gateway" ]] || fixture_fail "expected gateway posture, got '$posture'"

# --- trigger the polyglot pipeline with the minted api token -----------------
fixture_log "triggering test-pipeline via API"
TRIGGER_STATUS=$(curl -sS -o "$ARTIFACT_DIR/trigger-response.json" -w '%{http_code}' -X POST \
  "$API/pipeline/test-pipeline" \
  -H "Authorization: Bearer $API_TOKEN_VALUE" \
  -H 'Content-Type: application/json' \
  --data '{"payload":{"message":"hello from sys_exec"}}')
[[ "$TRIGGER_STATUS" == "202" ]] || fixture_fail "expected 202 for pipeline trigger, got $TRIGGER_STATUS"
JOB_ID=$(jq -r '.job_id' "$ARTIFACT_DIR/trigger-response.json")
fixture_log "job_id: $JOB_ID"

DB_PATH="$STATE_DIR/ductile.db"
STATUS_DB=""
for _ in $(seq 1 60); do
  if [[ -f "$DB_PATH" ]]; then
    STATUS_DB=$(sqlite3 "$DB_PATH" "SELECT status FROM job_log WHERE id LIKE '${JOB_ID}-%' LIMIT 1;" 2>/dev/null || true)
    [[ "$STATUS_DB" == "succeeded" || "$STATUS_DB" == "failed" ]] && break
  fi
  sleep 0.25
done
[[ "$STATUS_DB" == "succeeded" ]] || fixture_fail "expected job success, got '$STATUS_DB'"

# --- the subprocess wrote output.txt at its configured working_dir -----------
OUT_FILE="$STATE_DIR/output.txt"
[[ -f "$OUT_FILE" ]] || fixture_fail "output.txt not found at $OUT_FILE"
CONTENT=$(cat "$OUT_FILE")
[[ "$CONTENT" == "hello from sys_exec" ]] || fixture_fail "unexpected content in output.txt: '$CONTENT'"

fixture_log "success — vault-native polyglot round-trip: subprocess ran and wrote output.txt"
