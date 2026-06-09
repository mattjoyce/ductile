#!/usr/bin/env bash
# reload-lifecycle — LIVE hot-reload, VAULT-NATIVE. Proves the running daemon
# survives repeated `POST /system/reload` (listener stays up, plugins keep firing)
# — the live reload path the credential-ladder ACTIVATION (#130) depends on, which
# the in-process buildRuntime-swap tests don't exercise. The api token is minted
# from genesis via the credential ladder (no literal token).
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
API="http://127.0.0.1:18381"
API_TOKEN_VALUE="reload-api-admin-token"
SOCK="/tmp/dtl-reload-$$.sock"
CONFIG_SRC="$FIXTURE_DIR/config"
CONFIG_DIR="$ARTIFACT_DIR/runtime-config"
STATE_DIR="$CONFIG_DIR/state"
PID=""
SCENARIO_LOG="$ARTIFACT_DIR/scenario.log"

rm -rf "$CONFIG_DIR"
mkdir -p "$CONFIG_DIR" "$STATE_DIR"
cp -R "$CONFIG_SRC"/. "$CONFIG_DIR/"
mkdir -p "$CONFIG_DIR/plugins/echo"
cp "$ROOT_DIR/plugins/echo/manifest.yaml" "$CONFIG_DIR/plugins/echo/manifest.yaml"
cp "$ROOT_DIR/plugins/echo/run.sh" "$CONFIG_DIR/plugins/echo/run.sh"

exec > >(tee "$SCENARIO_LOG") 2>&1

cleanup() {
  if [[ -n "$PID" ]] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  rm -f "$SOCK"
  fixture_capture_tree "$STATE_DIR" state-dir
}
trap cleanup EXIT

wait_ready() {
  local label="$1" ready=0
  for _ in $(seq 1 60); do
    if curl -fsS --max-time 1 "$API/healthz" >"$ARTIFACT_DIR/healthz-${label}.json" 2>/dev/null; then
      ready=1; break
    fi
    sleep 0.25
  done
  [[ "$ready" -eq 1 ]] || fixture_fail "health endpoint did not become ready after $label"
}

reload_gateway() {
  local label="$1" status
  fixture_log "requesting reload: $label"
  if ! status=$(curl -sS --max-time 8 -o "$ARTIFACT_DIR/reload-${label}.json" -w '%{http_code}' -X POST \
    "$API/system/reload" -H "Authorization: Bearer $API_TOKEN_VALUE"); then
    fixture_fail "reload $label request failed"
  fi
  [[ "$status" == "200" ]] || fixture_fail "expected reload $label to return 200, got $status"
  local reload_status
  reload_status=$(jq -r '.status // empty' "$ARTIFACT_DIR/reload-${label}.json")
  [[ "$reload_status" == "ok" ]] || fixture_fail "expected reload $label status ok, got $reload_status"
}

trigger_plugin() {
  local label="$1" status
  fixture_log "triggering echo after $label"
  if ! status=$(curl -sS --max-time 5 -o "$ARTIFACT_DIR/plugin-${label}.json" -w '%{http_code}' -X POST \
    "$API/plugin/echo/poll" -H "Authorization: Bearer $API_TOKEN_VALUE" \
    -H 'Content-Type: application/json' --data '{"payload":{"message":"reload-lifecycle"}}'); then
    fixture_fail "plugin trigger after $label request failed"
  fi
  [[ "$status" == "202" ]] || fixture_fail "expected plugin trigger after $label to return 202, got $status"
  local job_id
  job_id=$(jq -r '.job_id // empty' "$ARTIFACT_DIR/plugin-${label}.json")
  [[ -n "$job_id" ]] || fixture_fail "plugin trigger after $label returned no job_id"
}

# --- genesis + bootstrap the api token via the management posture ------------
fixture_log "genesis: keygen + vault init"
fixture_vault_init "$CONFIG_DIR" >/dev/null
fixture_bootstrap_vault "$CONFIG_DIR" "$SOCK" ductile-api-admin "$API_TOKEN_VALUE"

fixture_log "starting ductile process (gateway)"
"$BIN" system start --config "$CONFIG_DIR" >"$ARTIFACT_DIR/ductile.log" 2>&1 &
PID=$!
wait_ready "initial"
posture="$(jq -r '.posture // empty' "$ARTIFACT_DIR/healthz-initial.json")"
[[ "$posture" == "gateway" ]] || fixture_fail "expected gateway posture, got '$posture'"
trigger_plugin "initial-start"

# A benign config edit (log level), then reload twice — the daemon must keep serving.
perl -0pi -e 's/log_level: info/log_level: debug/' "$CONFIG_DIR/config.yaml"

reload_gateway "first"
wait_ready "first-reload"
trigger_plugin "first-reload"

reload_gateway "second"
wait_ready "second-reload"
trigger_plugin "second-reload"

kill -0 "$PID" 2>/dev/null || fixture_fail "ductile process exited during reload lifecycle"

DB_PATH="$STATE_DIR/ductile.db"
TOTAL=0
for _ in $(seq 1 40); do
  if [[ -f "$DB_PATH" ]]; then
    TOTAL=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM job_log WHERE plugin = 'echo' AND command = 'poll';" 2>/dev/null || echo "0")
    [[ "$TOTAL" -ge 3 ]] && break
  fi
  sleep 0.25
done
printf '%s\n' "$TOTAL" > "$ARTIFACT_DIR/echo-job-log-count.txt"
[[ "$TOTAL" -ge 3 ]] || fixture_fail "expected at least 3 echo job logs across reloads, found $TOTAL"

fixture_log "success — vault-native: daemon survived two live /system/reload cycles, plugins kept firing"
