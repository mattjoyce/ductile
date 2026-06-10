#!/usr/bin/env bash
# Fixture: config-view-redaction — VAULT-NATIVE.
#
# Guards the secret-redaction guarantees that are LIVE at runtime. The redacted
# secrets are INLINE plugin-config values (nested.api_key, schedule payload.token)
# by design — that's what the redaction path protects; only the API TOKEN moves to
# the vault (minted from genesis via the credential ladder, no literal token).
#
#   Part A (F-006)    — /config/view redacts sensitive plugin config at any depth.
#   Part B (C-FRO-15) — the persisted config snapshot redacts AND fingerprints a
#     secret in a schedule payload.
#   Part C (C-FRO-15) — rotating only that redacted secret changes the snapshot
#     config_hash (drift signal preserved across a restart).
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
API="http://127.0.0.1:18561"
API_TOKEN_VALUE="cvr-api-admin-token"
SOCK="/tmp/dtl-cvr-$$.sock"
SCENARIO_LOG="$ARTIFACT_DIR/scenario.log"
exec > >(tee "$SCENARIO_LOG") 2>&1

WORK="$(mktemp -d)"
WORK="$(cd "$WORK" && pwd -P)"
CONFIG_DIR="$WORK/config"
STATE_DIR="$CONFIG_DIR/state"
DB_PATH="$STATE_DIR/ductile.db"
PLUGINS="$CONFIG_DIR/plugins.yaml"
PLUGINS_ORIG="$ARTIFACT_DIR/plugins.yaml.orig"
PID=""
cp -R "$FIXTURE_DIR/config/." "$CONFIG_DIR/"
mkdir -p "$STATE_DIR"
# Point the relative shared-plugins path at an absolute $ROOT_DIR/plugins (the work
# dir is in /tmp, so ../../../../../plugins no longer resolves). awk = portable.
awk -v r="$ROOT_DIR/plugins" '{gsub(/\.\.\/\.\.\/\.\.\/\.\.\/\.\.\/plugins/, r)}1' \
  "$CONFIG_DIR/config.yaml" > "$CONFIG_DIR/config.yaml.tmp" && mv "$CONFIG_DIR/config.yaml.tmp" "$CONFIG_DIR/config.yaml"
cp "$PLUGINS" "$PLUGINS_ORIG"

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

start_service() {
  "$BIN" system start --config "$CONFIG_DIR" >>"$ARTIFACT_DIR/ductile.log" 2>&1 &
  PID=$!
  local ready=0
  for _ in $(seq 1 60); do
    if curl -fsS "$API/healthz" >"$ARTIFACT_DIR/healthz.json" 2>/dev/null; then
      ready=1; break
    fi
    sleep 0.25
  done
  [[ "$ready" == "1" ]] || fixture_fail "health endpoint did not become ready"
  local posture
  posture="$(jq -r '.posture // empty' "$ARTIFACT_DIR/healthz.json")"
  [[ "$posture" == "gateway" ]] || fixture_fail "expected gateway posture, got '$posture'"
}

stop_service() {
  if [[ -n "$PID" ]] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  PID=""
}

latest_snapshot() { sqlite3 "$DB_PATH" "SELECT $1 FROM config_snapshots ORDER BY rowid DESC LIMIT 1;"; }
wait_for_snapshot() {
  for _ in $(seq 1 40); do
    local n; n=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM config_snapshots;" 2>/dev/null || echo 0)
    [[ "${n:-0}" -ge "${1:-1}" ]] && return 0
    sleep 0.25
  done
  fixture_fail "config_snapshots did not reach ${1:-1} row(s)"
}

# --- genesis + bootstrap the api token via the management posture ------------
fixture_log "genesis: keygen + vault init"
fixture_vault_init "$CONFIG_DIR" >/dev/null
fixture_bootstrap_vault "$CONFIG_DIR" "$SOCK" ductile-api-admin "$API_TOKEN_VALUE"

fixture_log "starting ductile process (gateway)"
start_service

# ---------- Part A — F-006: /config/view redacts nested plugin config ----------
fixture_log "Part A: /config/view nested plugin-config redaction (F-006)"
curl -sS "$API/config/view" -H "Authorization: Bearer $API_TOKEN_VALUE" >"$ARTIFACT_DIR/config-view.json"
nested=$(jq -r '.plugins.app.config.nested.api_key' "$ARTIFACT_DIR/config-view.json")
public=$(jq -r '.plugins.app.config.public' "$ARTIFACT_DIR/config-view.json")
[[ "$nested" == "[REDACTED]" ]] || fixture_fail "Part A: nested.api_key = '$nested', want '[REDACTED]'"
[[ "$public" == "visible-value" ]] || fixture_fail "Part A: public = '$public', want 'visible-value' (over-redaction)"
grep -q "PLAINTEXT_PLUGIN_NESTED" "$ARTIFACT_DIR/config-view.json" \
  && fixture_fail "Part A: /config/view leaked the nested plugin secret in plaintext"
fixture_log "Part A OK — nested secret redacted, non-secret preserved"

# ---------- Part B — C-FRO-15: snapshot redacts + fingerprints schedule payload ----------
fixture_log "Part B: config snapshot schedule-payload redaction + fingerprint (C-FRO-15)"
wait_for_snapshot 1
latest_snapshot sanitized_config    >"$ARTIFACT_DIR/sanitized_config.json"
latest_snapshot secret_fingerprints >"$ARTIFACT_DIR/secret_fingerprints.json"
grep -q "PLAINTEXT_SCHED_PAYLOAD" "$ARTIFACT_DIR/sanitized_config.json" \
  && fixture_fail "Part B: snapshot leaked the schedule payload secret in plaintext"
purposes=$(jq -r '.[].purpose' "$ARTIFACT_DIR/secret_fingerprints.json")
echo "$purposes" | grep -qE 'schedules\[0\]\.payload\.token' \
  || fixture_fail "Part B: schedule payload.token not in secret_fingerprints"
fixture_log "Part B OK — schedule-payload secret redacted AND fingerprinted"

# ---------- Part C — C-FRO-15: secret-only rotation flips config_hash ----------
fixture_log "Part C: secret-only rotation changes snapshot config_hash"
first_hash=$(latest_snapshot config_hash)
stop_service
sed 's/PLAINTEXT_SCHED_PAYLOAD/PLAINTEXT_SCHED_PAYLOAD_ROTATED/' "$PLUGINS_ORIG" >"$PLUGINS"
start_service
wait_for_snapshot 2
second_hash=$(latest_snapshot config_hash)
latest_snapshot sanitized_config >"$ARTIFACT_DIR/sanitized_config_after.json"
grep -q "PLAINTEXT_SCHED_PAYLOAD_ROTATED" "$ARTIFACT_DIR/sanitized_config_after.json" \
  && fixture_fail "Part C: rotated secret leaked in plaintext after restart"
[[ -n "$first_hash" && -n "$second_hash" ]] || fixture_fail "Part C: missing config_hash values"
[[ "$first_hash" != "$second_hash" ]] \
  || fixture_fail "Part C: config_hash unchanged after secret-only rotation ($first_hash) — drift not tracked"
fixture_log "Part C OK — config_hash changed on secret-only rotation"

fixture_log "success — vault-native: api token via the ladder; inline-secret redaction + snapshot fingerprint + rotation-drift all hold"
