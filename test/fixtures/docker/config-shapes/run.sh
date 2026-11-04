#!/usr/bin/env bash
# config-shapes — the privsep posture shapes and the ro-mount (Unraid) shape,
# each through `config check`, `config lock`, `system start` to /healthz and one
# `/system/reload` (card 153 slice 3). It is the baseline the Ashlar loader
# cutover (slice 4) is measured against, so it must pass on the current binary.
# The same config files are in the Go parity corpus (internal/config
# parity_corpus_test.go), so the struct comparison and this behaviour check read
# one source.
#
#   no-accounts       case 2: native, non-root. A root gateway holds
#                     CAP_SETUID and must refuse this shape, so root skips it.
#   unconfined        case 3: native, any uid.
#   accounts-enforce  case 1: Linux root. Creates accounts and runs the gateway
#                     as a non-root service uid carrying CAP_SETUID/CAP_SETGID.
#   ro-mount          case 4: Docker. Config bind-mounted :ro into the gateway;
#                     the lock runs in a one-shot read-write container.
#
# A case whose prerequisites are missing prints SKIP; a skip is not a pass, so
# the fixture exits 77 when any requested case skipped (the runner reports any
# non-zero exit as not passed). CONFIG_SHAPES_CASES="unconfined no-accounts"
# requests a subset, which passes when every requested case ran and passed.
#
# #174 (`config check` must agree with boot admission on integrity): the
# unconfined case edits config.yaml after the lock and requires `config check`
# and `system start` to refuse it alike. Card 153 expected this to document a
# gap; PR #176 already made `config check` mirror the admission policy, so the
# probe pins that agreement instead. A config.yaml edit is drift, a warning
# unless admission.fail_on_drift is set, so the unconfined and ro-mount configs
# set it.
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
HOST_BIN="$BIN"
CASES="${CONFIG_SHAPES_CASES:-no-accounts unconfined accounts-enforce ro-mount}"
API_TOKEN_VALUE="config-shapes-api-admin-token"
PID=""
CONTAINERS=()
SOCKETS=()
CASE1_WORK=""
CASE1_CREATED=()
SKIPPED=()
EXIT_SKIPPED=77
SCENARIO_LOG="$ARTIFACT_DIR/scenario.log"
exec > >(tee "$SCENARIO_LOG") 2>&1

cleanup() {
  if [[ -n "$PID" ]] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  local c s
  for c in "${CONTAINERS[@]+"${CONTAINERS[@]}"}"; do
    docker rm -f "$c" >/dev/null 2>&1 || true
  done
  for s in "${SOCKETS[@]+"${SOCKETS[@]}"}"; do
    rm -f "$s"
  done
  case1_cleanup
}
trap cleanup EXIT

skip_case() {
  fixture_log "SKIP $1 — $2 (a skip is NOT a pass)"
  SKIPPED+=("$1")
}

# stage_case <case> <dest>: copy the case config, the echo plugin and a state
# dir, then substitute the placeholders the Go corpus also substitutes.
stage_case() {
  local name="$1" dest="$2"
  rm -rf "$dest"
  mkdir -p "$dest/plugins/echo" "$dest/state"
  cp "$FIXTURE_DIR/$name"/*.yaml "$dest/"
  cp "$ROOT_DIR/plugins/echo/manifest.yaml" "$ROOT_DIR/plugins/echo/run.sh" "$dest/plugins/echo/"
  local f
  for f in "$dest"/*.yaml; do
    sed -i.bak -e "s#__CASE_DIR__#$dest#g" \
      -e "s#__WORKER_UID__#${WORKER_UID:-4181}#g" -e "s#__WORKER_GID__#${WORKER_GID:-4181}#g" \
      -e "s#__UNTRUSTED_UID__#${UNTRUSTED_UID:-4182}#g" -e "s#__UNTRUSTED_GID__#${UNTRUSTED_GID:-4182}#g" "$f"
    rm -f "$f.bak"
  done
}

api_listen() {
  awk '/^[[:space:]]*listen:/ {gsub(/[",]/,"",$2); print $2; exit}' "$1/api.yaml"
}

# config_check <label> <config dir>: valid (0) or valid-with-warnings (2).
config_check() {
  local label="$1" cfg="$2" rc
  set +e
  "$BIN" config check --config-dir "$cfg" >"$ARTIFACT_DIR/$label-check.log" 2>&1
  rc=$?
  set -e
  [[ "$rc" -eq 0 || "$rc" -eq 2 ]] \
    || fixture_fail "$label: config check rc=$rc: $(cat "$ARTIFACT_DIR/$label-check.log")"
  fixture_log "$label: config check rc=$rc"
}

lock_case() {
  local label="$1" cfg="$2"
  "$BIN" config lock --config-dir "$cfg" >"$ARTIFACT_DIR/$label-config-lock.log" 2>&1 \
    || fixture_fail "$label: config lock failed: $(cat "$ARTIFACT_DIR/$label-config-lock.log")"
  "$BIN" plugin lock echo --config-dir "$cfg" >"$ARTIFACT_DIR/$label-plugin-lock.log" 2>&1 \
    || fixture_fail "$label: plugin lock echo failed: $(cat "$ARTIFACT_DIR/$label-plugin-lock.log")"
  fixture_log "$label: config lock + plugin lock echo"
}

wait_healthz() {
  local label="$1" api="$2" i
  for i in $(seq 1 80); do
    if curl -fsS --max-time 1 "http://$api/healthz" >"$ARTIFACT_DIR/$label-healthz.json" 2>/dev/null; then
      fixture_log "$label: /healthz ok"
      return 0
    fi
    sleep 0.25
  done
  fixture_fail "$label: /healthz never answered on $api"
}

reload_ok() {
  local label="$1" api="$2" status
  status=$(curl -sS --max-time 10 -o "$ARTIFACT_DIR/$label-reload.json" -w '%{http_code}' -X POST \
    "http://$api/system/reload" -H "Authorization: Bearer $API_TOKEN_VALUE") \
    || fixture_fail "$label: reload request failed"
  [[ "$status" == "200" ]] || fixture_fail "$label: reload returned $status: $(cat "$ARTIFACT_DIR/$label-reload.json")"
  [[ "$(jq -r '.status // empty' "$ARTIFACT_DIR/$label-reload.json")" == "ok" ]] \
    || fixture_fail "$label: reload status is not ok: $(cat "$ARTIFACT_DIR/$label-reload.json")"
  fixture_log "$label: /system/reload ok"
}

stop_gateway() {
  if [[ -n "$PID" ]] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  PID=""
}

# run_native_case <case> <expected posture log line>: genesis, lock, check,
# bootstrap the api token, check again, boot, assert the posture line, reload.
# The first check follows the lock: with fail_on_drift set, a tree with no
# .checksums is invalid.
run_native_case() {
  local name="$1" want="$2"
  local cfg="$ARTIFACT_DIR/$name/config" sock="/tmp/dcs-$name-$$.sock" api
  SOCKETS+=("$sock")
  stage_case "$name" "$cfg"
  api="$(api_listen "$cfg")"
  fixture_vault_init "$cfg" >/dev/null
  lock_case "$name" "$cfg"
  config_check "$name-locked" "$cfg"
  fixture_bootstrap_vault "$cfg" "$sock" ductile-api-admin "$API_TOKEN_VALUE"
  config_check "$name" "$cfg"

  "$BIN" system start --config "$cfg" >"$ARTIFACT_DIR/$name-gateway.log" 2>&1 &
  PID=$!
  wait_healthz "$name" "$api"
  grep -qF "$want" "$ARTIFACT_DIR/$name-gateway.log" \
    || fixture_fail "$name: posture line missing; want '$want': $(cat "$ARTIFACT_DIR/$name-gateway.log")"
  fixture_log "$name: posture line present"
  reload_ok "$name" "$api"
  wait_healthz "$name-after-reload" "$api"
  stop_gateway
}

# probe_174 <config dir>: edit a locked file; `config check` and `system start`
# must both refuse it, each naming the hash mismatch (#174).
probe_174() {
  local cfg="$1" rc check_rc
  printf '# edited after lock (#174 probe)\n' >> "$cfg/config.yaml"
  set +e
  "$BIN" config check --config-dir "$cfg" >"$ARTIFACT_DIR/174-check.log" 2>&1
  check_rc=$?
  timeout 20 "$BIN" system start --config "$cfg" >"$ARTIFACT_DIR/174-start.log" 2>&1
  rc=$?
  set -e
  [[ "$rc" -ne 0 && "$rc" -ne 124 ]] \
    || fixture_fail "#174 probe: system start booted a tree edited after its lock (rc=$rc)"
  [[ "$check_rc" -eq 1 ]] \
    || fixture_fail "#174 probe: config check accepted (rc=$check_rc) a tree system start refuses (rc=$rc)"
  grep -q "hash mismatch" "$ARTIFACT_DIR/174-check.log" \
    || fixture_fail "#174 probe: config check refused without naming the hash mismatch: $(cat "$ARTIFACT_DIR/174-check.log")"
  grep -q "hash mismatch" "$ARTIFACT_DIR/174-start.log" \
    || fixture_fail "#174 probe: system start refused without naming the hash mismatch: $(tail -5 "$ARTIFACT_DIR/174-start.log")"
  fixture_log "#174 probe: config check (rc=$check_rc) and system start (rc=$rc) both refuse the edited tree"
}

case_no_accounts() {
  if [[ "$(id -u)" -eq 0 ]]; then
    skip_case no-accounts "root holds CAP_SETUID, so the no-accounts shape refuses by design; run as a non-root user"
    return
  fi
  run_native_case no-accounts "privsep posture: UNCONFINED — no accounts configured"
}

case_unconfined() {
  run_native_case unconfined "privsep UNCONFINED: plugins run at the gateway uid despite configuration"
  probe_174 "$ARTIFACT_DIR/unconfined/config"
}

# shellcheck source=/dev/null
source "$FIXTURE_DIR/case-accounts-enforce.sh"
# shellcheck source=/dev/null
source "$FIXTURE_DIR/case-ro-mount.sh"

for name in $CASES; do
  fixture_log "case $name"
  case "$name" in
    no-accounts) case_no_accounts ;;
    unconfined) case_unconfined ;;
    accounts-enforce)
      # Case 1 needs root, and case 2 must not run as root, so when this run is
      # not root but has passwordless sudo (GitHub runners, the Dell), case 1
      # runs alone in a child under sudo; a laptop without it skips (exit 77).
      if [[ "$(id -u)" -ne 0 ]] && sudo -n true 2>/dev/null; then
        fixture_log "accounts-enforce: not root, running this case under sudo"
        child_rc=0
        CONFIG_SHAPES_CASES=accounts-enforce FIXTURE_NAME="${FIXTURE_NAME}-accounts-enforce" \
          sudo -n --preserve-env=ROOT_DIR,FIXTURE_NAME,ARTIFACT_ROOT,CONFIG_SHAPES_CASES "$0" \
          || child_rc=$?
        case "$child_rc" in
          0) fixture_log "accounts-enforce: passed under sudo" ;;
          "$EXIT_SKIPPED") SKIPPED+=(accounts-enforce) ;;
          *) fixture_fail "accounts-enforce failed under sudo (rc=$child_rc)" ;;
        esac
      else
        case_accounts_enforce
      fi
      ;;
    ro-mount) case_ro_mount ;;
    *) fixture_fail "unknown case $name" ;;
  esac
done

if [[ "${#SKIPPED[@]}" -gt 0 ]]; then
  fixture_log "NOT PASSED: skipped ${SKIPPED[*]} — those shapes were NOT verified (exit $EXIT_SKIPPED)"
  exit "$EXIT_SKIPPED"
fi
fixture_log "success — every requested config shape checked, locked, booted and reloaded: $CASES"
