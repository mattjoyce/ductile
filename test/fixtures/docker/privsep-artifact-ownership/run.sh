#!/usr/bin/env bash
# privsep-artifact-ownership — the LIVE ownership + posture property
# (#167, #169, #170, #180).
#
# The unit tests prove the owner-negotiation mechanism. This fixture proves the
# thing the operator actually cares about: that a privileged CLI write leaves an
# artifact the *service account* can read, and that when it cannot, admission says
# so accurately instead of blaming a missing manifest.
#
# It needs a second uid, so it needs root. That is the whole reason this class of
# bug survived: writer and reader are the same uid on a laptop and in every test
# that has ever run, so the defect is invisible until a privsep host boots.
#
# Scenario 0 is the control: it runs the PRE-FIX binary and asserts the bug is
# still there. A harness that cannot detect the bug it was written for is not a
# harness, and this one has to earn the claim.
set -euo pipefail

ROOT_DIR="${ROOT_DIR:?}"
FIXTURE_NAME="${FIXTURE_NAME:?}"
ARTIFACT_ROOT="${ARTIFACT_ROOT:?}"
# shellcheck source=/dev/null
source "$ROOT_DIR/scripts/test-docker-lib"
fixture_init

# Re-exec under sudo when we are not root but could be. GitHub runners and the
# Dell both have passwordless sudo; a dev laptop does not, and there the fixture
# says plainly that it did not run rather than reporting a pass.
if [[ "$(id -u)" -ne 0 ]]; then
  if sudo -n true 2>/dev/null; then
    fixture_log "not root — re-executing under sudo"
    exec sudo -n --preserve-env=ROOT_DIR,FIXTURE_NAME,ARTIFACT_ROOT "$0" "$@"
  fi
  fixture_log "SKIP — needs root to create a service account and change ownership."
  fixture_log "SKIP — this fixture is the only coverage for the #167 class; a skip is NOT a pass."
  exit 0
fi

BIN="$ROOT_DIR/ductile"
BIN_PREFIX="${DUCTILE_PREFIX_BIN:-}"   # optional pre-fix binary for the control case
SCENARIO_LOG="$ARTIFACT_DIR/scenario.log"
exec > >(tee "$SCENARIO_LOG") 2>&1

SVC_USER="ductilesvc"
SVC_UID=4167
SVC_GID=4167
WORKER_USER="ductileworker"
WORKER_UID=4267
WORKER_GID=4267

WORK="$(mktemp -d)"
WORK="$(cd "$WORK" && pwd -P)"
# mktemp -d is 0700 root:root. Without traverse permission on the parent, the
# service account cannot reach a file it legitimately owns, which would look
# exactly like the #167 outage and make every scenario below a false positive.
chmod 0755 "$WORK"

# $BIN normally sits in the checkout, which on a CI runner lives under a home
# directory the service account cannot traverse. Running it via `su` then fails
# with rc=126 (permission denied) — which is non-zero and contains no
# missing-manifest text, so the refusal assertions below would pass VACUOUSLY.
# That is precisely what happened on the first CI run of this fixture. Copy the
# binary somewhere the service account can actually reach.
SVC_BIN="$WORK/ductile"
cp "$BIN" "$SVC_BIN"
chmod 0755 "$SVC_BIN"
CAP_BIN="$WORK/ductile-cap"

# run_as_service <label> <command...> — runs as the service account and refuses to
# let an exec failure masquerade as a program result. Sets RUN_OUT and RUN_RC.
run_as_service() {
  local label="$1"; shift
  set +e
  RUN_OUT="$(su -s /bin/sh -c "$*" "$SVC_USER" 2>&1)"
  RUN_RC=$?
  set -e
  if [[ "$RUN_RC" -eq 126 || "$RUN_RC" -eq 127 ]]; then
    fixture_fail "$label: could not execute as $SVC_USER (rc=$RUN_RC) — that is a harness problem, not a refusal: $RUN_OUT"
  fi
}
cleanup() {
  rm -rf "$WORK"
  userdel "$WORKER_USER" 2>/dev/null || true
  groupdel "$WORKER_USER" 2>/dev/null || true
  userdel "$SVC_USER" 2>/dev/null || true
  groupdel "$SVC_USER" 2>/dev/null || true
}
trap cleanup EXIT

# --- service account -------------------------------------------------------
if ! getent group "$SVC_USER" >/dev/null; then
  groupadd -g "$SVC_GID" "$SVC_USER"
fi
if ! getent passwd "$SVC_USER" >/dev/null; then
  useradd -u "$SVC_UID" -g "$SVC_GID" -M -s /usr/sbin/nologin "$SVC_USER"
fi
fixture_log "service account: $SVC_USER ($SVC_UID:$SVC_GID); this process is uid $(id -u)"
if ! getent group "$WORKER_USER" >/dev/null; then
  groupadd -g "$WORKER_GID" "$WORKER_USER"
fi
if ! getent passwd "$WORKER_USER" >/dev/null; then
  useradd -u "$WORKER_UID" -g "$WORKER_GID" -M -s /usr/sbin/nologin "$WORKER_USER"
fi
fixture_log "plugin account: $WORKER_USER ($WORKER_UID:$WORKER_GID)"

# --- helpers ---------------------------------------------------------------

owner_of() { stat -c '%u:%g' "$1"; }

# write_privsep_config <dir> <verify_on_boot> <fail_on_drift>
# A config dir shaped like a privsep install: owned by the service account, with
# an accounts table so the boot gate can reach enforce.
write_privsep_config() {
  local d="$1" verify="$2" drift="$3"
  mkdir -p "$d/plugins" "$d/state"
  cat > "$d/config.yaml" <<EOF
service:
  strict_mode: false
  unconfined: true
  admission:
    verify_integrity_on_boot: $verify
    fail_on_drift: $drift
    validate_config_on_boot: true
state:
  path: "./state/ductile.db"
plugin_roots:
  - "./plugins"
accounts:
  default:
    uid: $SVC_UID
    gid: $SVC_GID
    state_dir: "$d/state/accounts/default"
EOF
  mkdir -p "$d/state/accounts/default"
  chown -R "$SVC_UID:$SVC_GID" "$d"
}

# write_gate_config <dir> [account_state_dir]
# Writes the two boot-gate refusal shapes without the unconfined override. When
# account_state_dir is absent, a capable gateway must refuse because there is
# nowhere to drop. When present, a plain gateway must refuse because it cannot
# build the declared wall.
write_gate_config() {
  local d="$1" account_state="${2:-}"
  mkdir -p "$d/plugins" "$d/state"
  cat > "$d/config.yaml" <<EOF
service:
  strict_mode: false
  admission:
    verify_integrity_on_boot: false
    fail_on_drift: false
    validate_config_on_boot: false
state:
  path: "./state/ductile.db"
plugin_roots:
  - "./plugins"
plugins: {}
EOF
  if [[ -n "$account_state" ]]; then
    cat >> "$d/config.yaml" <<EOF
accounts:
  default:
    uid: $WORKER_UID
    gid: $WORKER_GID
    state_dir: "$account_state"
EOF
    mkdir -p "$account_state"
    chown "$WORKER_UID:$WORKER_GID" "$account_state"
    chmod 0700 "$account_state"
  fi
  chown -R "$SVC_UID:$SVC_GID" "$d"
}

# write_enforce_config <config_dir> <plugin_root> <account_state_dir>
# The plugin root deliberately lives outside config_dir. Enforce reconciliation
# tightens config_dir to 0700 gateway-owned; a confined plugin must execute from
# a separately traversable, read-only root and write only in its own state_dir.
write_enforce_config() {
  local d="$1" plugin_root="$2" account_state="$3"
  mkdir -p "$d/state" "$plugin_root/posture_probe" "$account_state"
  cat > "$plugin_root/posture_probe/manifest.yaml" <<'EOF'
manifest_spec: ductile.plugin
manifest_version: 1
name: posture_probe
version: 0.1.0
protocol: 2
entrypoint: run.sh
description: "Records the real uid, gid, capabilities, and core-delivered config."
commands:
  - name: poll
    type: write
    description: "Record the confined runtime identity."
config_keys:
  required: [artifact]
  optional: []
EOF
  cat > "$plugin_root/posture_probe/run.sh" <<'EOF'
#!/bin/sh
set -eu

request="$(cat)"
artifact="$(printf '%s' "$request" | jq -r '.config.artifact // empty')"
if [ -z "$artifact" ]; then
  printf '%s\n' '{"status":"error","error":"missing config.artifact","retry":false}'
  exit 0
fi
cap_eff="$(awk '/^CapEff:/ { print $2 }' /proc/self/status)"
printf '%s %s %s %s\n' "$(id -u)" "$(id -g)" "$cap_eff" "$artifact" > "$HOME/enforce-probe"
printf '%s\n' '{"status":"ok","result":"enforce probe recorded","events":[],"logs":[]}'
EOF
  chmod 0755 "$plugin_root" "$plugin_root/posture_probe" "$plugin_root/posture_probe/run.sh"
  chmod 0644 "$plugin_root/posture_probe/manifest.yaml"

  cat > "$d/config.yaml" <<EOF
service:
  tick_interval: 100ms
  strict_mode: false
  admission:
    # Scenarios 1–5 exercise integrity admission. Keeping it off here avoids
    # coupling the posture proof to vault-keyed plugin attestation: this scenario
    # owns the capability/accounts agreement and the real dropped spawn.
    verify_integrity_on_boot: false
    fail_on_drift: false
    validate_config_on_boot: true
state:
  path: "./state/ductile.db"
plugin_roots:
  - "$plugin_root"
plugins:
  posture_probe:
    enabled: true
    run_as: default
    schedules:
      - every: 200ms
    config:
      artifact: unset
accounts:
  default:
    uid: $WORKER_UID
    gid: $WORKER_GID
    state_dir: "$account_state"
EOF
  chown -R "$SVC_UID:$SVC_GID" "$d"
  chown "$WORKER_UID:$WORKER_GID" "$account_state"
  chmod 0700 "$account_state"
}

# assert_owner <label> <path> <want uid:gid>
assert_owner() {
  local label="$1" path="$2" want="$3" got
  [[ -e "$path" ]] || fixture_fail "$label: $path does not exist"
  got="$(owner_of "$path")"
  [[ "$got" == "$want" ]] || fixture_fail "$label: $(basename "$path") owned by $got, want $want"
  fixture_log "$label OK — $(basename "$path") owned by $got"
}

# assert_readable_by_service <label> <path>
# Distinguishes "cannot traverse the directory" from "cannot read the file". They
# present identically to the daemon — both are EACCES — but only the second is
# #167. Conflating them is how a harness bug masquerades as a product bug.
assert_readable_by_service() {
  local label="$1" path="$2" dir
  dir="$(dirname "$path")"
  if ! su -s /bin/sh -c "test -x '$dir'" "$SVC_USER"; then
    fixture_fail "$label: $SVC_USER cannot traverse $dir (mode $(stat -c '%a %u:%g' "$dir")) — harness setup problem, not #167"
  fi
  if ! su -s /bin/sh -c "test -r '$path'" "$SVC_USER"; then
    fixture_fail "$label: $SVC_USER cannot read $path (mode $(stat -c '%a %u:%g' "$path")) — this is the #167 outage"
  fi
  fixture_log "$label OK — $SVC_USER can read $(basename "$path")"
}

# --- Scenario 0: control — the PRE-FIX binary must still exhibit the bug ----
# Without this the whole fixture could be vacuously green.
if [[ -n "$BIN_PREFIX" && -x "$BIN_PREFIX" ]]; then
  C0="$WORK/control-prefix"; write_privsep_config "$C0" true true
  "$BIN_PREFIX" config lock --config-dir "$C0" >"$ARTIFACT_DIR/control-lock.log" 2>&1 || true
  if [[ -f "$C0/.checksums" ]]; then
    got="$(owner_of "$C0/.checksums")"
    if [[ "$got" == "$SVC_UID:$SVC_GID" ]]; then
      fixture_fail "control: pre-fix binary produced a correctly-owned manifest ($got) — the harness cannot detect #167"
    fi
    fixture_log "control OK — pre-fix binary reproduced #167: .checksums owned by $got, not $SVC_UID:$SVC_GID"
  else
    fixture_log "control SKIP — pre-fix binary wrote no manifest"
  fi
else
  fixture_log "control SKIP — set DUCTILE_PREFIX_BIN to a pre-fix binary to prove the harness detects #167"
fi

# --- Scenario 1 (#167): root locks, service account must be able to read ----
C1="$WORK/lock-as-root"; write_privsep_config "$C1" true true
"$BIN" config lock --config-dir "$C1" >"$ARTIFACT_DIR/s1-lock.log" 2>&1 \
  || fixture_fail "s1: config lock failed as root: $(cat "$ARTIFACT_DIR/s1-lock.log")"
assert_owner "s1" "$C1/.checksums" "$SVC_UID:$SVC_GID"
assert_readable_by_service "s1" "$C1/.checksums"

# --- Scenario 2 (#167): a root-owned manifest is diagnosed, not misreported --
# Stage the broken state an existing install would already be in.
C2="$WORK/root-owned-manifest"; write_privsep_config "$C2" true true
"$BIN" config lock --config-dir "$C2" >/dev/null 2>&1
chown 0:0 "$C2/.checksums"
chmod 600 "$C2/.checksums"
run_as_service "s2" "timeout 20 '$SVC_BIN' system start --config '$C2'"
printf '%s\n' "$RUN_OUT" > "$ARTIFACT_DIR/s2-start.log"
[[ "$RUN_RC" -ne 0 ]] || fixture_fail "s2: daemon booted with an unreadable manifest — integrity gate did not fail closed"
if printf '%s' "$RUN_OUT" | grep -qi "no .checksums manifest found"; then
  fixture_fail "s2: EACCES still reported as a missing manifest (#167 read side): $RUN_OUT"
fi
printf '%s' "$RUN_OUT" | grep -qi "permission denied" \
  || fixture_fail "s2: refusal did not name the permission cause; got: $RUN_OUT"
fixture_log "s2 OK — refused (rc=$RUN_RC) naming permission, not a missing manifest"

# --- Scenario 3 (#167): self-healing — re-locking as root repairs ownership --
"$BIN" config lock --config-dir "$C2" >"$ARTIFACT_DIR/s3-relock.log" 2>&1 \
  || fixture_fail "s3: config lock failed on the broken install: $(cat "$ARTIFACT_DIR/s3-relock.log")"
assert_owner "s3" "$C2/.checksums" "$SVC_UID:$SVC_GID"
assert_readable_by_service "s3" "$C2/.checksums"

# --- Scenario 4 (#169): config writes land service-owned ---------------------
# Two writes, so the second produces the .bak sidecar — which carries the same
# ownership trap one restore away.
C4="$WORK/config-write"; write_privsep_config "$C4" true true

# config set exits 2 when the resulting config validates with warnings, which this
# fixture's config does by design (unconfined override on a configured accounts
# table). Only a genuine failure should fail the scenario.
config_set() {
  local label="$1" kv="$2" rc
  set +e
  "$BIN" config set "$kv" --apply --config-dir "$C4" >"$ARTIFACT_DIR/${label}.log" 2>&1
  rc=$?
  set -e
  [[ "$rc" -eq 0 || "$rc" -eq 2 ]] \
    || fixture_fail "s4: config set $kv failed rc=$rc (#169 coverage cannot be skipped): $(cat "$ARTIFACT_DIR/${label}.log")"
  grep -qi "successfully set" "$ARTIFACT_DIR/${label}.log" \
    || fixture_fail "s4: config set $kv did not report success: $(cat "$ARTIFACT_DIR/${label}.log")"
}
config_set "s4-set1" "service.log_level=debug"
config_set "s4-set2" "service.log_level=warn"

assert_owner "s4" "$C4/config.yaml" "$SVC_UID:$SVC_GID"
assert_readable_by_service "s4" "$C4/config.yaml"

# `config set` persists via config.SetPath, which truncates the existing file in
# place (internal/config/access.go:264) and so preserves its owner by accident of
# os.WriteFile semantics rather than by negotiation. It never reaches
# writeFileAtomicWithBackup, so it does NOT cover #169. Route mutations do — they
# create routes.yaml fresh, which is the case where a new inode takes the
# *writer's* uid and the daemon is locked out.
"$BIN" config route add --from echo --event tick --to echo --config-dir "$C4" \
  >"$ARTIFACT_DIR/s4-route1.log" 2>&1 || true
if [[ -f "$C4/routes.yaml" ]]; then
  assert_owner "s4-routes" "$C4/routes.yaml" "$SVC_UID:$SVC_GID"
  assert_readable_by_service "s4-routes" "$C4/routes.yaml"
  "$BIN" config route add --from echo --event tock --to echo --config-dir "$C4" \
    >"$ARTIFACT_DIR/s4-route2.log" 2>&1 || true
  [[ -f "$C4/routes.yaml.bak" ]] \
    || fixture_fail "s4: no .bak after a second route write; #169's sidecar path went unexercised"
  assert_owner "s4-routes-bak" "$C4/routes.yaml.bak" "$SVC_UID:$SVC_GID"
else
  fixture_fail "s4: route add wrote no routes.yaml — #169's writer went unexercised: $(head -3 "$ARTIFACT_DIR/s4-route1.log")"
fi

# --- Scenario 5: admission matrix -------------------------------------------
# verify_integrity_on_boot x fail_on_drift, each against a root-owned manifest.
# The message must never be the loop-inducing one, whatever the policy.
for verify in true false; do
  for drift in true false; do
    label="s5-verify=${verify}-drift=${drift}"
    D="$WORK/$label"; write_privsep_config "$D" "$verify" "$drift"
    "$BIN" config lock --config-dir "$D" >/dev/null 2>&1
    chown 0:0 "$D/.checksums"; chmod 600 "$D/.checksums"
    run_as_service "$label" "timeout 20 '$SVC_BIN' system start --config '$D'"
    printf '%s\n' "$RUN_OUT" > "$ARTIFACT_DIR/${label}.log"
    if printf '%s' "$RUN_OUT" | grep -qi "no .checksums manifest found"; then
      fixture_fail "$label: still misreports EACCES as a missing manifest"
    fi
    # With verification on, the daemon must actually refuse and say why. With it
    # off it is expected to boot (and be killed by timeout, rc=124). Asserting the
    # shape here is what stops an unrelated non-zero exit reading as a pass.
    if [[ "$verify" == "true" ]]; then
      [[ "$RUN_RC" -ne 0 ]] || fixture_fail "$label: expected refusal with verify_integrity_on_boot=true"
      printf '%s' "$RUN_OUT" | grep -qi "permission denied" \
        || fixture_fail "$label: refusal did not name the permission cause; got: $RUN_OUT"
    else
      [[ "$RUN_RC" -eq 124 ]] || fixture_fail "$label: expected the daemon to boot (timeout kill, rc=124) with verification off; got rc=$RUN_RC: $RUN_OUT"
    fi
    fixture_log "$label OK — rc=$RUN_RC, behaviour matches the admission policy"
  done
done

# --- Scenario 6: unprivileged writes must not regress ------------------------
# The Docker/NFS/userns shape. An unprivileged caller writing into a directory it
# does not own must still succeed — aborting there would break working installs
# to fix a privsep bug they do not have. This is the one design decision in the
# #167 fix that was previously an assumption.
C6="$WORK/unprivileged"; write_privsep_config "$C6" true true
chmod 0777 "$C6"
run_as_service "s6" "'$SVC_BIN' config lock --config-dir '$C6'"
printf '%s\n' "$RUN_OUT" > "$ARTIFACT_DIR/s6-lock.log"
[[ "$RUN_RC" -eq 0 ]] || fixture_fail "s6: unprivileged config lock regressed (rc=$RUN_RC): $RUN_OUT"
fixture_log "s6 OK — unprivileged lock still succeeds; no regression for Docker/NFS/userns installs"

# --- capability setup for true non-root enforce (#180) -----------------------
# File capabilities are the fixture equivalent of the shipped systemd unit's
# AmbientCapabilities. The executable is root-owned and carries ONLY the two
# capabilities the gateway needs to drop a child; its uid remains the service uid.
command -v setcap >/dev/null 2>&1 \
  || fixture_fail "s7: setcap is required to exercise non-root enforce (install libcap2-bin)"
command -v getcap >/dev/null 2>&1 \
  || fixture_fail "s7: getcap is required to verify the capability-bearing binary"
cp "$BIN" "$CAP_BIN"
chown 0:0 "$CAP_BIN"
chmod 0755 "$CAP_BIN"
setcap cap_setuid,cap_setgid+eip "$CAP_BIN" \
  || fixture_fail "s7: could not attach CAP_SETUID/CAP_SETGID to $CAP_BIN"
CAP_DESC="$(getcap "$CAP_BIN")"
printf '%s' "$CAP_DESC" | grep -q 'cap_setgid' \
  || fixture_fail "s7: capable binary is missing CAP_SETGID: $CAP_DESC"
printf '%s' "$CAP_DESC" | grep -q 'cap_setuid' \
  || fixture_fail "s7: capable binary is missing CAP_SETUID: $CAP_DESC"
SERVICE_EXEC_UID="$(su -s /bin/sh -c "id -u" "$SVC_USER")"
[[ "$SERVICE_EXEC_UID" == "$SVC_UID" ]] \
  || fixture_fail "s7: su launched uid $SERVICE_EXEC_UID, want genuinely non-root uid $SVC_UID"
fixture_log "capability setup OK — non-root uid $SERVICE_EXEC_UID executes with $CAP_DESC"

# --- Scenario 7 (#180): capability + no accounts must REFUSE ----------------
C7="$WORK/capability-no-accounts"; write_gate_config "$C7"
run_as_service "s7" "timeout 20 '$CAP_BIN' system start --config '$C7'"
printf '%s\n' "$RUN_OUT" > "$ARTIFACT_DIR/s7-capability-no-accounts.log"
[[ "$RUN_RC" -ne 0 && "$RUN_RC" -ne 124 ]] \
  || fixture_fail "s7: capable non-root gateway booted without accounts (rc=$RUN_RC)"
printf '%s' "$RUN_OUT" | grep -qi "holds the uid-drop capability but no accounts are configured" \
  || fixture_fail "s7: wrong refusal for capability + no accounts: $RUN_OUT"
fixture_log "s7 OK — capable non-root gateway refused to boot without accounts"

# --- Scenario 8 (#180): accounts + no capability must REFUSE ----------------
C8="$WORK/accounts-no-capability"
C8_ACCOUNT_STATE="$WORK/s8-account-state"
write_gate_config "$C8" "$C8_ACCOUNT_STATE"
[[ -z "$(getcap "$SVC_BIN")" ]] \
  || fixture_fail "s8: plain binary unexpectedly carries file capabilities: $(getcap "$SVC_BIN")"
run_as_service "s8" "timeout 20 '$SVC_BIN' system start --config '$C8'"
printf '%s\n' "$RUN_OUT" > "$ARTIFACT_DIR/s8-accounts-no-capability.log"
[[ "$RUN_RC" -ne 0 && "$RUN_RC" -ne 124 ]] \
  || fixture_fail "s8: capability-free gateway booted with accounts configured (rc=$RUN_RC)"
printf '%s' "$RUN_OUT" | grep -qi "accounts are configured but the gateway lacks the uid-drop capability" \
  || fixture_fail "s8: wrong refusal for accounts + no capability: $RUN_OUT"
fixture_log "s8 OK — capability-free gateway refused a wall it could not enforce"

# --- Scenario 9 (#180): non-root + capabilities + accounts reaches ENFORCE --
# A privileged config edit writes config.yaml, then a privileged lock writes the
# manifest. The service uid must read both, and the scheduled plugin must receive
# the edited config through the core protocol after dropping to the worker uid.
C9="$WORK/enforce"
C9_PLUGIN_ROOT="$WORK/enforce-plugins"
C9_ACCOUNT_STATE="$WORK/enforce-account-state"
write_enforce_config "$C9" "$C9_PLUGIN_ROOT" "$C9_ACCOUNT_STATE"
set +e
"$BIN" config set "plugins.posture_probe.config.artifact=privileged-cli-artifact" \
  --apply --config-dir "$C9" >"$ARTIFACT_DIR/s9-config-set.log" 2>&1
C9_SET_RC=$?
set -e
[[ "$C9_SET_RC" -eq 0 || "$C9_SET_RC" -eq 2 ]] \
  || fixture_fail "s9: privileged config edit failed (rc=$C9_SET_RC): $(cat "$ARTIFACT_DIR/s9-config-set.log")"
grep -qi "successfully set" "$ARTIFACT_DIR/s9-config-set.log" \
  || fixture_fail "s9: privileged config edit did not report success: $(cat "$ARTIFACT_DIR/s9-config-set.log")"
assert_owner "s9-config" "$C9/config.yaml" "$SVC_UID:$SVC_GID"
"$BIN" config lock --config-dir "$C9" >"$ARTIFACT_DIR/s9-lock.log" 2>&1 \
  || fixture_fail "s9: privileged config lock failed: $(cat "$ARTIFACT_DIR/s9-lock.log")"
assert_owner "s9-lock" "$C9/.checksums" "$SVC_UID:$SVC_GID"
assert_readable_by_service "s9-lock" "$C9/.checksums"

run_as_service "s9" "timeout 10 '$CAP_BIN' system start --config '$C9'"
printf '%s\n' "$RUN_OUT" > "$ARTIFACT_DIR/s9-enforce.log"
[[ "$RUN_RC" -eq 124 ]] \
  || fixture_fail "s9: enforce gateway did not stay booted until timeout (rc=$RUN_RC): $RUN_OUT"
printf '%s' "$RUN_OUT" | grep -q "privsep enforcing: plugins drop to their resolved account" \
  || fixture_fail "s9: gateway never announced enforce posture: $RUN_OUT"
if printf '%s' "$RUN_OUT" | grep -q "privsep UNCONFINED"; then
  fixture_fail "s9: gateway announced UNCONFINED during an enforce scenario: $RUN_OUT"
fi

C9_MARKER="$C9_ACCOUNT_STATE/enforce-probe"
[[ -f "$C9_MARKER" ]] \
  || fixture_fail "s9: confined plugin wrote no marker; it did not complete a real spawn"
read -r OBS_UID OBS_GID OBS_CAP_EFF OBS_ARTIFACT < "$C9_MARKER"
[[ "$OBS_UID" == "$WORKER_UID" && "$OBS_GID" == "$WORKER_GID" ]] \
  || fixture_fail "s9: plugin ran as $OBS_UID:$OBS_GID, want dropped account $WORKER_UID:$WORKER_GID"
[[ "$OBS_CAP_EFF" =~ ^0+$ ]] \
  || fixture_fail "s9: dropped plugin retained effective capabilities: $OBS_CAP_EFF"
[[ "$OBS_ARTIFACT" == "privileged-cli-artifact" ]] \
  || fixture_fail "s9: plugin did not receive the privileged CLI config value: $OBS_ARTIFACT"
fixture_log "s9 OK — uid $SVC_UID + two caps reached enforce; plugin dropped to $OBS_UID:$OBS_GID with CapEff=$OBS_CAP_EFF"

fixture_log "success — ownership paths hold, both boot-gate refusals are live, and non-root capability-backed enforce drops a real plugin"
