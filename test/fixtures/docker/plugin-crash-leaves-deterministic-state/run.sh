#!/usr/bin/env bash
# plugin-crash-leaves-deterministic-state — a plugin subprocess dies hard
# (uncatchable self-SIGKILL) MID-job and the gateway stays deterministic:
#   - the job lands in a terminal state (failed), not stuck running/queued,
#   - the daemon keeps serving AND keeps dispatching (a second job round-trips),
#   - the state dir holds no non-terminal queue rows at the end.
# Live-only: this is the real exec.Cmd.Wait death path + queue bookkeeping
# against a booted gateway — in-process tests can't kill a real child mid-job.
# Vault-native boot via the credential ladder (no literal token).
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
API="http://127.0.0.1:18681"
API_TOKEN_VALUE="crash-api-admin-token"
SOCK="/tmp/dtl-crash-$$.sock"
SCENARIO_LOG="$ARTIFACT_DIR/scenario.log"
exec > >(tee "$SCENARIO_LOG") 2>&1

WORK="$(mktemp -d)"
WORK="$(cd "$WORK" && pwd -P)"
CONFIG_DIR="$WORK/config"
STATE_DIR="$CONFIG_DIR/state"
PID=""
cp -R "$FIXTURE_DIR/config/." "$CONFIG_DIR/"
chmod +x "$CONFIG_DIR/plugins/crash_once/run.py" 2>/dev/null || true
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
fixture_log "config lock + plugin lock crash_once"
"$BIN" config lock --config "$CONFIG_DIR" >"$ARTIFACT_DIR/lock.log" 2>&1
"$BIN" plugin lock crash_once --config "$CONFIG_DIR" >>"$ARTIFACT_DIR/lock.log" 2>&1

fixture_log "boot gateway"
# Plugin processes inherit ductile's cwd; running from CONFIG_DIR anchors the
# relative marker_file path (./state/...) to the fixture's config tree.
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

DB_PATH="$STATE_DIR/ductile.db"

# trigger_crash_job <label> -> echoes job_id
trigger_crash_job() {
  local label="$1" status
  status=$(curl -sS -o "$ARTIFACT_DIR/trigger-$label.json" -w '%{http_code}' -X POST \
    "$API/pipeline/crash-pipeline" \
    -H "Authorization: Bearer $API_TOKEN_VALUE" \
    -H 'Content-Type: application/json' \
    --data "{\"payload\":{\"round\":\"$label\"}}")
  [[ "$status" == "202" ]] || fixture_fail "expected 202 for crash trigger ($label), got $status"
  jq -r '.job_id' "$ARTIFACT_DIR/trigger-$label.json"
}

# wait_terminal <job_id> <label> -> echoes the job_log status
wait_terminal() {
  local job_id="$1" label="$2" st=""
  for _ in $(seq 1 120); do
    if [[ -f "$DB_PATH" ]]; then
      st=$(sqlite3 "$DB_PATH" "SELECT status FROM job_log WHERE id LIKE '${job_id}-%' ORDER BY attempt DESC LIMIT 1;" 2>/dev/null || true)
      [[ -n "$st" ]] && break
    fi
    sleep 0.25
  done
  [[ -n "$st" ]] || fixture_fail "job ($label) never reached a terminal state"
  printf '%s' "$st"
}

# --- round 1: the plugin crashes mid-job -> terminal failed -------------------
fixture_log "round 1: trigger crash-pipeline"
JOB1=$(trigger_crash_job round1)
fixture_log "round 1 job_id: $JOB1"
STATUS1=$(wait_terminal "$JOB1" round1)
fixture_log "round 1 terminal status: $STATUS1"
[[ "$STATUS1" == "failed" ]] || fixture_fail "expected terminal status 'failed' after crash, got '$STATUS1'"

MARKER="$STATE_DIR/crash-started.marker"
[[ -f "$MARKER" ]] || fixture_fail "started-marker missing — plugin crashed before doing work?"
fixture_log "started-marker present (job '$(cat "$MARKER")') — crash happened mid-job"

# --- the daemon survived the crash --------------------------------------------
curl -fsS "$API/healthz" >"$ARTIFACT_DIR/healthz-after-crash.json" \
  || fixture_fail "gateway stopped serving /healthz after the plugin crash"
fixture_log "gateway still serving after crash"

# --- round 2: dispatching still works (not a wedged loop) ---------------------
fixture_log "round 2: trigger crash-pipeline again"
JOB2=$(trigger_crash_job round2)
STATUS2=$(wait_terminal "$JOB2" round2)
fixture_log "round 2 terminal status: $STATUS2"
[[ "$STATUS2" == "failed" ]] || fixture_fail "expected terminal status 'failed' on round 2, got '$STATUS2'"

# --- deterministic state dir: nothing left non-terminal -----------------------
RESIDUAL=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM job_queue WHERE status NOT IN ('succeeded','failed','timed_out','dead','skipped');")
[[ "$RESIDUAL" == "0" ]] || fixture_fail "expected 0 non-terminal queue rows, found $RESIDUAL"
sqlite3 "$DB_PATH" "SELECT id, status, attempt FROM job_queue;" >"$ARTIFACT_DIR/job_queue.txt"

fixture_log "success — crash mid-job left deterministic state: terminal failed, live daemon, clean queue"
