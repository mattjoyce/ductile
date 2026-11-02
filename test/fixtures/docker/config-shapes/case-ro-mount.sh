# shellcheck shell=bash
# config-shapes case 4: the ro-mount flow. Sourced by run.sh.
#
# The Unraid shape (docs/runbooks/unraid-docker-upgrade.md, "How a container
# host differs" and the one-shot lock): the gateway container mounts the config
# directory read-only, so `docker exec ... config lock` fails, a one-shot
# read-write container locks instead, and the running gateway's reload admits
# the new manifest. Everything runs in containers as the invoking uid; the only
# host paths mounted are inside this checkout (the Thinkpad lane's rule).
#
# CONFIG_SHAPES_LINUX_BIN may name a prebuilt linux ductile binary inside the
# checkout. Otherwise the case cross-compiles with the host Go toolchain, and if
# that fails builds in golang:1.26-alpine with only the checkout mounted.

CASE4_IMAGE="${CONFIG_SHAPES_IMAGE:-alpine:3.20}"
CASE4_GO_IMAGE="${CONFIG_SHAPES_GO_IMAGE:-golang:1.26-alpine}"

case4_linux_bin() {
  local out="$1" arch
  if [[ -n "${CONFIG_SHAPES_LINUX_BIN:-}" ]]; then
    cp "$CONFIG_SHAPES_LINUX_BIN" "$out"
    return 0
  fi
  arch="$(docker info --format '{{.Architecture}}')"
  case "$arch" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) fixture_fail "ro-mount: unsupported docker architecture $arch" ;;
  esac
  if (cd "$ROOT_DIR" && GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -o "$out" ./cmd/ductile) \
    >"$ARTIFACT_DIR/ro-mount-build.log" 2>&1; then
    return 0
  fi
  fixture_log "ro-mount: host cross-compile failed; building in $CASE4_GO_IMAGE"
  docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -e GOCACHE=/tmp/gocache \
    -e GOMODCACHE=/tmp/gomodcache -e CGO_ENABLED=0 -v "$ROOT_DIR":/src -w /src \
    "$CASE4_GO_IMAGE" go build -o "/src/${out#"$ROOT_DIR"/}" ./cmd/ductile \
    >>"$ARTIFACT_DIR/ro-mount-build.log" 2>&1 \
    || fixture_fail "ro-mount: could not build a linux binary: $(tail -5 "$ARTIFACT_DIR/ro-mount-build.log")"
}

# case4_run <rw|ro> <ductile args...>: a one-shot container with the config
# mounted at /app/config in the given mode and the state dir beside it.
case4_run() {
  local mode="$1"
  shift
  docker run --rm -i --user "$(id -u):$(id -g)" -e HOME=/tmp \
    -v "$CASE4_CFG":/app/config:"$mode" -v "$CASE4_STATE":/app/state \
    -v "$CASE4_BIN":/app/ductile:ro "$CASE4_IMAGE" /app/ductile "$@"
}

# case4_start <name> <rw|ro>: a detached gateway container.
case4_start() {
  local name="$1" mode="$2"
  CONTAINERS+=("$name")
  docker run -d --name "$name" --user "$(id -u):$(id -g)" -e HOME=/tmp \
    -v "$CASE4_CFG":/app/config:"$mode" -v "$CASE4_STATE":/app/state \
    -v "$CASE4_BIN":/app/ductile:ro "$CASE4_IMAGE" \
    /app/ductile system start --config /app/config/config.yaml >/dev/null
}

case4_wget() {
  docker exec "$1" wget -qO- --timeout=5 "${@:2}"
}

case4_wait_healthz() {
  local name="$1" label="$2" api="$3" i
  for i in $(seq 1 80); do
    if case4_wget "$name" "http://$api/healthz" >"$ARTIFACT_DIR/$label-healthz.json" 2>/dev/null; then
      fixture_log "$label: /healthz ok"
      return 0
    fi
    sleep 0.25
  done
  docker logs "$name" >"$ARTIFACT_DIR/$label-container.log" 2>&1 || true
  fixture_fail "$label: /healthz never answered: $(tail -20 "$ARTIFACT_DIR/$label-container.log")"
}

case4_reload() {
  case4_wget "$1" --header "Authorization: Bearer $API_TOKEN_VALUE" --post-data '' "http://$2/system/reload"
}

# case4_bootstrap: the credential ladder inside containers. Genesis in a
# one-shot rw container, then the management posture in a detached rw container
# where the api token is minted over the unix socket.
case4_bootstrap() {
  local api="$1" sock="/tmp/dcs-ro.sock" mgmt="dcs-ro-mgmt-$$" i admin
  case4_run rw secrets keygen -out /app/config/age.key >/dev/null 2>>"$ARTIFACT_DIR/genesis.log"
  chmod 600 "$CASE4_CFG/age.key"
  admin="$(case4_run rw vault init --vault /app/config/vault.age --key /app/config/age.key 2>>"$ARTIFACT_DIR/genesis.log")"
  [[ -n "$admin" ]] || fixture_fail "ro-mount: vault init produced no admin token"
  case4_run rw config lock --config-dir /app/config >"$ARTIFACT_DIR/ro-mount-config-lock.log" 2>&1 \
    || fixture_fail "ro-mount: config lock failed: $(cat "$ARTIFACT_DIR/ro-mount-config-lock.log")"
  case4_run rw plugin lock echo --config-dir /app/config >"$ARTIFACT_DIR/ro-mount-plugin-lock.log" 2>&1 \
    || fixture_fail "ro-mount: plugin lock failed: $(cat "$ARTIFACT_DIR/ro-mount-plugin-lock.log")"
  local rc=0
  case4_run ro config check --config-dir /app/config >"$ARTIFACT_DIR/ro-mount-locked-check.log" 2>&1 || rc=$?
  [[ "$rc" -eq 0 || "$rc" -eq 2 ]] \
    || fixture_fail "ro-mount: config check rc=$rc: $(cat "$ARTIFACT_DIR/ro-mount-locked-check.log")"
  fixture_log "ro-mount: config check on the :ro mount rc=$rc"

  printf '  management_socket: "%s"\n' "$sock" >>"$CASE4_CFG/api.yaml"
  case4_start "$mgmt" rw
  for i in $(seq 1 80); do
    if printf '%s' "$API_TOKEN_VALUE" | docker exec -i -e DUCTILE_VAULT_TOKEN="$admin" "$mgmt" \
      /app/ductile vault set --api-url "unix://$sock" --name ductile-api-admin --pattern manual \
      >>"$ARTIFACT_DIR/ro-mount-vault-set.log" 2>&1; then
      break
    fi
    [[ "$i" -lt 80 ]] || fixture_fail "ro-mount: could not mint the api token: $(tail -5 "$ARTIFACT_DIR/ro-mount-vault-set.log")"
    sleep 0.25
  done
  docker rm -f "$mgmt" >/dev/null
  cat >>"$CASE4_CFG/api.yaml" <<EOF
  auth:
    tokens:
      - secret_ref: ductile-api-admin
        scopes: ["*"]
EOF
  fixture_log "ro-mount: api token minted in the management posture"
  [[ -n "$api" ]] || fixture_fail "ro-mount: no api listen address"
}

case_ro_mount() {
  if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
    skip_case ro-mount "needs a working docker"
    return
  fi
  local work="$ARTIFACT_DIR/ro-mount" gw="dcs-ro-gw-$$" api rc
  CASE4_CFG="$work/config"
  CASE4_STATE="$work/state"
  CASE4_BIN="$work/ductile-linux"
  stage_case ro-mount "$CASE4_CFG"
  rmdir "$CASE4_CFG/state"
  mkdir -p "$CASE4_STATE"
  case4_linux_bin "$CASE4_BIN"
  api="$(api_listen "$CASE4_CFG")"
  case4_bootstrap "$api"

  case4_start "$gw" ro
  case4_wait_healthz "$gw" ro-mount "$api"
  docker logs "$gw" >"$ARTIFACT_DIR/ro-mount-gateway.log" 2>&1
  grep -qF "privsep UNCONFINED: plugins run at the gateway uid despite configuration" "$ARTIFACT_DIR/ro-mount-gateway.log" \
    || fixture_fail "ro-mount: posture line missing: $(tail -20 "$ARTIFACT_DIR/ro-mount-gateway.log")"

  set +e
  docker exec "$gw" /app/ductile config lock --config-dir /app/config >"$ARTIFACT_DIR/ro-mount-exec-lock.log" 2>&1
  rc=$?
  set -e
  [[ "$rc" -ne 0 ]] || fixture_fail "ro-mount: config lock succeeded through a read-only mount"
  fixture_log "ro-mount: docker exec config lock refused on the :ro mount (rc=$rc)"

  printf '# edited on the host after the lock\n' >>"$CASE4_CFG/config.yaml"
  if case4_reload "$gw" "$api" >"$ARTIFACT_DIR/ro-mount-reload-stale.json" 2>&1; then
    fixture_fail "ro-mount: reload admitted a config edited after its lock: $(cat "$ARTIFACT_DIR/ro-mount-reload-stale.json")"
  fi
  fixture_log "ro-mount: reload refused the edited config before the re-lock"

  case4_run rw config lock --config-dir /app/config >"$ARTIFACT_DIR/ro-mount-relock.log" 2>&1 \
    || fixture_fail "ro-mount: one-shot rw lock failed: $(cat "$ARTIFACT_DIR/ro-mount-relock.log")"
  case4_reload "$gw" "$api" >"$ARTIFACT_DIR/ro-mount-reload.json" 2>&1 \
    || fixture_fail "ro-mount: reload after the one-shot lock failed: $(cat "$ARTIFACT_DIR/ro-mount-reload.json")"
  [[ "$(jq -r '.status // empty' "$ARTIFACT_DIR/ro-mount-reload.json")" == "ok" ]] \
    || fixture_fail "ro-mount: reload status is not ok: $(cat "$ARTIFACT_DIR/ro-mount-reload.json")"
  case4_wait_healthz "$gw" ro-mount-after-reload "$api"
  fixture_log "ro-mount: one-shot rw lock admitted by the running gateway's reload"
  docker rm -f "$gw" >/dev/null
}
