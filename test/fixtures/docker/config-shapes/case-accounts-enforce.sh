# shellcheck shell=bash
# config-shapes case 1: accounts + enforce. Sourced by run.sh.
#
# The account scaffolding follows privsep-artifact-ownership/run.sh: a service
# uid runs a copy of the gateway that carries only CAP_SETUID and CAP_SETGID as
# file capabilities (the fixture's stand-in for the systemd unit's
# AmbientCapabilities), and two plugin accounts exist for the accounts map.
# Every ductile command, the vault ladder included, runs as the service uid
# through a wrapper, so every artifact is service-owned as on the Thinkpad.

CASE1_SVC_USER="dcsvc"
CASE1_SVC_UID=4180
WORKER_UID=4181
WORKER_GID=4181
UNTRUSTED_UID=4182
UNTRUSTED_GID=4182

case1_make_user() {
  local user="$1" id="$2"
  getent group "$user" >/dev/null || groupadd -g "$id" "$user"
  getent passwd "$user" >/dev/null || useradd -u "$id" -g "$id" -M -s /usr/sbin/nologin "$user"
}

case_accounts_enforce() {
  if [[ "$(uname -s)" != "Linux" ]]; then
    skip_case accounts-enforce "needs Linux (accounts, file capabilities, su)"
    return
  fi
  if [[ "$(id -u)" -ne 0 ]]; then
    skip_case accounts-enforce "needs root to create accounts and set file capabilities"
    return
  fi
  local tool
  for tool in setcap getcap su useradd groupadd; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      skip_case accounts-enforce "missing $tool"
      return
    fi
  done

  local work
  work="$(mktemp -d)"
  work="$(cd "$work" && pwd -P)"
  chmod 0755 "$work"
  CASE1_CLEANUP="rm -rf '$work'; for u in dcdefault dcuntrusted $CASE1_SVC_USER; do userdel \"\$u\" 2>/dev/null || true; groupdel \"\$u\" 2>/dev/null || true; done"
  case1_make_user "$CASE1_SVC_USER" "$CASE1_SVC_UID"
  case1_make_user dcdefault "$WORKER_UID"
  case1_make_user dcuntrusted "$UNTRUSTED_UID"

  local cap_bin="$work/ductile-cap" wrapper="$work/ductile-svc"
  cp "$HOST_BIN" "$cap_bin"
  chown 0:0 "$cap_bin"
  chmod 0755 "$cap_bin"
  setcap cap_setuid,cap_setgid+eip "$cap_bin" || fixture_fail "accounts-enforce: setcap failed"
  getcap "$cap_bin" | grep -q cap_setuid || fixture_fail "accounts-enforce: capability missing on $cap_bin"
  cat >"$wrapper" <<EOF
#!/bin/bash
cmd="\$(printf '%q ' '$cap_bin' "\$@")"
exec su -s /bin/bash -c "\$cmd" $CASE1_SVC_USER
EOF
  chmod 0755 "$wrapper"

  local cfg="$work/config"
  stage_case accounts-enforce "$cfg"
  mkdir -p "$cfg/accounts/default" "$cfg/accounts/untrusted"
  chown -R "$CASE1_SVC_UID:$CASE1_SVC_UID" "$cfg"
  chown "$WORKER_UID:$WORKER_GID" "$cfg/accounts/default"
  chown "$UNTRUSTED_UID:$UNTRUSTED_GID" "$cfg/accounts/untrusted"
  chmod 0700 "$cfg/accounts/default" "$cfg/accounts/untrusted"

  local saved_bin="$BIN" sock="/tmp/dcs-enforce-$$.sock" api
  SOCKETS+=("$sock")
  BIN="$wrapper"
  api="$(api_listen "$cfg")"
  fixture_vault_init "$cfg" >/dev/null
  lock_case accounts-enforce "$cfg"
  config_check accounts-enforce-locked "$cfg"
  fixture_bootstrap_vault "$cfg" "$sock" ductile-api-admin "$API_TOKEN_VALUE"
  config_check accounts-enforce "$cfg"

  "$BIN" system start --config "$cfg" >"$ARTIFACT_DIR/accounts-enforce-gateway.log" 2>&1 &
  PID=$!
  wait_healthz accounts-enforce "$api"
  grep -qF "privsep enforcing: plugins drop to their resolved account" "$ARTIFACT_DIR/accounts-enforce-gateway.log" \
    || fixture_fail "accounts-enforce: gateway never announced enforce: $(cat "$ARTIFACT_DIR/accounts-enforce-gateway.log")"
  if grep -q "privsep UNCONFINED" "$ARTIFACT_DIR/accounts-enforce-gateway.log"; then
    fixture_fail "accounts-enforce: gateway announced UNCONFINED in the enforce shape"
  fi
  fixture_log "accounts-enforce: posture line present (uid $CASE1_SVC_UID + two capabilities)"
  reload_ok accounts-enforce "$api"
  wait_healthz accounts-enforce-after-reload "$api"
  stop_gateway
  BIN="$saved_bin"
}
