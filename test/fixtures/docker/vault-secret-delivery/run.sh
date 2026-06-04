#!/usr/bin/env bash
# vault-secret-delivery — black-box acceptance for the vault stack.
#
# Proves the spawn-time secret path end to end against a real running gateway:
#   keygen -> genesis -> config lock + plugin lock (keyed attestation) -> boot ->
#   register principal -> grant secret -> dispatch -> secret delivered over stdin.
# Also asserts the local read surface: `vault get` returns a normal secret but
# REFUSES the reserved admin token (card #42), and both reads are audited.
#
# All genesis/lock artifacts (age key, vault blob, .checksums, state) are created
# in a throwaway mktemp copy of the config so the committed fixture stays pristine.
set -euo pipefail

ROOT_DIR="${ROOT_DIR:?}"
FIXTURE_NAME="${FIXTURE_NAME:?}"
ARTIFACT_ROOT="${ARTIFACT_ROOT:?}"
# shellcheck source=/dev/null
source "$ROOT_DIR/scripts/test-docker-lib"
fixture_init

BIN="$ROOT_DIR/ductile"
API="http://127.0.0.1:18181"
SCENARIO_LOG="$ARTIFACT_DIR/scenario.log"
exec > >(tee "$SCENARIO_LOG") 2>&1

WORK="$(mktemp -d)"
# Canonicalize: on macOS $TMPDIR lives under /var -> /private/var, and a symlinked
# plugin_root is refused by discovery (service.allow_symlinks). pwd -P is a no-op on
# Linux where /tmp is already canonical.
WORK="$(cd "$WORK" && pwd -P)"
CONFIG_DIR="$WORK/config"
PID=""
cp -R "$FIXTURE_DIR/config/." "$CONFIG_DIR/"
chmod +x "$CONFIG_DIR/plugins/secret-probe/run.sh"
mkdir -p "$CONFIG_DIR/state"

cleanup() {
  if [[ -n "$PID" ]] && kill -0 "$PID" 2>/dev/null; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  fixture_capture_tree "$CONFIG_DIR" config
  rm -rf "$WORK"
}
trap cleanup EXIT

fixture_log "genesis: keygen + vault init"
"$BIN" secrets keygen -out "$CONFIG_DIR/age.key" >/dev/null 2>>"$ARTIFACT_DIR/genesis.log"
chmod 600 "$CONFIG_DIR/age.key"
ADMIN="$("$BIN" vault init --vault "$CONFIG_DIR/vault.age" --key "$CONFIG_DIR/age.key" 2>>"$ARTIFACT_DIR/genesis.log")"
[[ -n "$ADMIN" ]] || fixture_fail "vault init produced no admin token"
export DUCTILE_VAULT_TOKEN="$ADMIN"

fixture_log "attestation: config lock + plugin lock secret-probe"
"$BIN" config lock --config "$CONFIG_DIR" >>"$ARTIFACT_DIR/lock.log" 2>&1
"$BIN" plugin lock secret-probe --config "$CONFIG_DIR" >>"$ARTIFACT_DIR/lock.log" 2>&1

fixture_log "boot gateway"
"$BIN" system start --config "$CONFIG_DIR" >"$ARTIFACT_DIR/ductile.log" 2>&1 &
PID=$!
ready=0
for _ in $(seq 1 60); do
  curl -fsS "$API/healthz" >"$ARTIFACT_DIR/healthz.json" 2>/dev/null && { ready=1; break; }
  sleep 0.25
done
[[ "$ready" -eq 1 ]] || fixture_fail "health endpoint did not become ready"
grep -q "compose-time attestation on" "$ARTIFACT_DIR/ductile.log" \
  || fixture_fail "expected compose-time attestation to be enabled at boot"

fixture_log "register principal + grant secret my-secret=hunter2"
"$BIN" vault register-principal --api-url "$API" --name secret-probe --kind plugin
printf 'hunter2' | "$BIN" vault set --api-url "$API" --name my-secret --principal secret-probe

fixture_log "dispatch secret-probe and confirm the secret arrived over stdin"
"$BIN" api /plugin/secret-probe/poll --config "$CONFIG_DIR" -X POST -f trigger=1 >"$ARTIFACT_DIR/dispatch.json"
JOB="$(jq -r '.job_id // empty' "$ARTIFACT_DIR/dispatch.json")"
[[ -n "$JOB" ]] || fixture_fail "dispatch returned no job_id"

status=""
for _ in $(seq 1 60); do
  status="$("$BIN" job logs --config "$CONFIG_DIR" --plugin secret-probe --json 2>/dev/null | jq -r '.logs[0].Status // empty')"
  [[ "$status" == "succeeded" ]] && break
  [[ "$status" == "failed" ]] && fixture_fail "secret-probe job failed"
  sleep 0.25
done
[[ "$status" == "succeeded" ]] || fixture_fail "secret-probe job did not succeed (status=$status)"

RESULT="$("$BIN" job logs --config "$CONFIG_DIR" --plugin secret-probe --include-result --json | jq -r '.logs[0].Result.result // empty')"
echo "$RESULT" >"$ARTIFACT_DIR/probe-result.txt"
EXPECT='count=1 names=[my-secret] value_lens=[my-secret:7]'
[[ "$RESULT" == "$EXPECT" ]] \
  || fixture_fail "secret not delivered as expected; got: '$RESULT' want: '$EXPECT'"

fixture_log "vault get: normal secret returns its value"
GOT="$("$BIN" vault get --config "$CONFIG_DIR" --name my-secret 2>/dev/null)"
[[ "$GOT" == "hunter2" ]] || fixture_fail "vault get my-secret returned '$GOT', want 'hunter2'"

fixture_log "vault get: reserved admin token is REFUSED (#42)"
set +e
REFUSE_OUT="$("$BIN" vault get --config "$CONFIG_DIR" --name core-admin-token 2>&1)"
REFUSE_RC=$?
set -e
[[ "$REFUSE_RC" -ne 0 ]] || fixture_fail "vault get core-admin-token should have been refused"
echo "$REFUSE_OUT" | grep -qi "reserved" \
  || fixture_fail "refusal should mention 'reserved'; got: $REFUSE_OUT"

fixture_log "audit: register + read/denied facts recorded"
AUDIT="$("$BIN" system vault-audit --config "$CONFIG_DIR" 2>&1)"
echo "$AUDIT" >"$ARTIFACT_DIR/vault-audit.txt"
echo "$AUDIT" | grep -q "register" || fixture_fail "audit missing register fact"
echo "$AUDIT" | grep -q "denied"   || fixture_fail "audit missing read/denied fact for the reserved read"

fixture_log "success — secret delivered over stdin; reserved read refused + audited"
