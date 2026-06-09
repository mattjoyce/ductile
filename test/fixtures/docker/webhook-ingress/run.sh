#!/usr/bin/env bash
# webhook-ingress — black-box acceptance for HMAC-verified webhook ingress,
# VAULT-NATIVE. The webhook HMAC secret and the api token both live in the vault
# (tokens.yaml is retired); they are minted from genesis via the credential ladder:
#   keygen + genesis -> management posture (mint api token + github_webhook_secret) ->
#   config lock + plugin lock -> gateway -> valid webhook 202 / invalid 403 / job queued.
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
API="http://127.0.0.1:18081"
WEBHOOK="http://127.0.0.1:18091"
API_TOKEN_VALUE="whi-api-admin-token"
WEBHOOK_SECRET="test-secret"
# Short absolute socket path (unix sun_path ~104B cap — keep it off the long temp dir).
SOCK="/tmp/dtl-whi-$$.sock"
SCENARIO_LOG="$ARTIFACT_DIR/scenario.log"
exec > >(tee "$SCENARIO_LOG") 2>&1

WORK="$(mktemp -d)"
WORK="$(cd "$WORK" && pwd -P)"
CONFIG_DIR="$WORK/config"
STATE_DIR="$CONFIG_DIR/state"
PID=""
cp -R "$FIXTURE_DIR/config/." "$CONFIG_DIR/"
chmod +x "$CONFIG_DIR/plugins/echo/run.sh"
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

# --- genesis + bootstrap the vault secrets via the management posture ---------
fixture_log "genesis: keygen + vault init"
fixture_vault_init "$CONFIG_DIR" >/dev/null
# Mint the api token AND the webhook HMAC secret (both load-visible: no principal),
# proving the from-scratch path serves NO public/webhook plane in the management posture.
fixture_bootstrap_vault "$CONFIG_DIR" "$SOCK" ductile-api-admin "$API_TOKEN_VALUE" \
  "github_webhook_secret=$WEBHOOK_SECRET"

# --- activation: now that the webhook secret exists, include webhooks.yaml ----
printf '  - webhooks.yaml\n' >> "$CONFIG_DIR/config.yaml"

# --- seal + boot the gateway -------------------------------------------------
fixture_log "config lock + plugin lock echo"
"$BIN" config lock --config "$CONFIG_DIR" >"$ARTIFACT_DIR/config-lock.log" 2>&1
"$BIN" plugin lock echo --config "$CONFIG_DIR" >>"$ARTIFACT_DIR/config-lock.log" 2>&1

fixture_log "boot gateway"
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

# --- valid webhook: HMAC over the vault-resolved secret -> 202 ----------------
fixture_log "sending valid webhook (HMAC over the vault-minted secret)"
BODY='{"event":"push"}'
SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" -hex | awk '{print $2}')
VALID_STATUS=$(curl -sS -o "$ARTIFACT_DIR/valid-response.json" -w '%{http_code}' -X POST \
  "$WEBHOOK/webhook/github" \
  -H "X-Hub-Signature-256: sha256=$SIG" \
  -H 'Content-Type: application/json' \
  --data "$BODY")
[[ "$VALID_STATUS" == "202" ]] || fixture_fail "expected 202 for valid webhook, got $VALID_STATUS"

# --- invalid signature -> 403 ------------------------------------------------
fixture_log "sending invalid webhook"
INVALID_STATUS=$(curl -sS -o "$ARTIFACT_DIR/invalid-response.json" -w '%{http_code}' -X POST \
  "$WEBHOOK/webhook/github" \
  -H 'X-Hub-Signature-256: sha256=deadbeef' \
  -H 'Content-Type: application/json' \
  --data "$BODY")
[[ "$INVALID_STATUS" == "403" ]] || fixture_fail "expected 403 for invalid webhook, got $INVALID_STATUS"

# --- the valid webhook enqueued an echo job ----------------------------------
DB_PATH="$STATE_DIR/ductile.db"
COUNT=0
for _ in $(seq 1 40); do
  if [[ -f "$DB_PATH" ]]; then
    COUNT=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM job_queue WHERE plugin = 'echo' AND command = 'handle';" 2>/dev/null || echo "0")
    [[ "$COUNT" -ge 1 ]] && break
  fi
  sleep 0.25
done
echo "$COUNT" > "$ARTIFACT_DIR/job-count.txt"
[[ "$COUNT" -ge 1 ]] || fixture_fail "expected queued webhook job, found $COUNT"

fixture_log "success — vault-native: webhook secret + api token minted via the ladder; valid 202 / invalid 403 / job enqueued"
