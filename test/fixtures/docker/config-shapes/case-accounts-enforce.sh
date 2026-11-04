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

CASE1_ACCOUNTS=("$CASE1_SVC_USER:$CASE1_SVC_UID" "dcdefault:$WORKER_UID" "dcuntrusted:$UNTRUSTED_UID")

# case1_refuse_collisions fails before anything is created when any account
# name, group name, uid or gid this case would create already exists, so the
# cleanup can never delete an account it did not make.
case1_refuse_collisions() {
  local entry name id
  for entry in "${CASE1_ACCOUNTS[@]}"; do
    name="${entry%%:*}"
    id="${entry##*:}"
    if getent passwd "$name" >/dev/null || getent group "$name" >/dev/null ||
      getent passwd "$id" >/dev/null || getent group "$id" >/dev/null; then
      fixture_fail "accounts-enforce: refusing to run — account or id $name/$id already exists on this host; this case creates and deletes it"
    fi
  done
}

# case1_make_user creates a group and a user and records each the moment it
# exists, so an early failure still deletes exactly what this case made.
case1_make_user() {
  local user="$1" id="$2"
  groupadd -g "$id" "$user" || fixture_fail "accounts-enforce: groupadd $user failed"
  CASE1_CREATED+=("group:$user")
  useradd -u "$id" -g "$id" -M -s /usr/sbin/nologin "$user" || fixture_fail "accounts-enforce: useradd $user failed"
  CASE1_CREATED+=("user:$user")
}

# case1_cleanup removes the work dir and, newest first, only the accounts and
# groups this case recorded as created. run.sh's EXIT trap calls it.
case1_cleanup() {
  local i entry
  if [[ -n "$CASE1_WORK" ]]; then
    rm -rf "$CASE1_WORK"
  fi
  for ((i = ${#CASE1_CREATED[@]} - 1; i >= 0; i--)); do
    entry="${CASE1_CREATED[$i]}"
    case "$entry" in
      user:*) userdel "${entry#user:}" 2>/dev/null || true ;;
      group:*) groupdel "${entry#group:}" 2>/dev/null || true ;;
    esac
  done
  CASE1_CREATED=()
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

  case1_refuse_collisions
  local work entry
  work="$(mktemp -d)"
  CASE1_WORK="$work"
  work="$(cd "$work" && pwd -P)"
  CASE1_WORK="$work"
  chmod 0755 "$work"
  for entry in "${CASE1_ACCOUNTS[@]}"; do
    case1_make_user "${entry%%:*}" "${entry##*:}"
  done

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

  # Account state dirs live outside the config dir, as on the Thinkpad
  # (/var/lib/ductile/accounts): the gateway keeps its config dir 0700, so a
  # state dir beneath it is unreachable by the accounts (readability check).
  local cfg="$work/config" state="$work/accounts"
  STATE_ROOT="$state" stage_case accounts-enforce "$cfg"
  mkdir -p "$state/default" "$state/untrusted"
  chmod 0755 "$state"
  chown -R "$CASE1_SVC_UID:$CASE1_SVC_UID" "$cfg"
  chown "$WORKER_UID:$WORKER_GID" "$state/default"
  chown "$UNTRUSTED_UID:$UNTRUSTED_GID" "$state/untrusted"
  chmod 0700 "$state/default" "$state/untrusted"

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
