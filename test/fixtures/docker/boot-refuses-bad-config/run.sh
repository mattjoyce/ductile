#!/usr/bin/env bash
# boot-refuses-bad-config — the LIVE fail-closed property (#119/#94). A real
# `system start` must REFUSE an unsafe config (non-zero exit, never a half-boot),
# not just be rejected by an in-process validator. Each case generates a throwaway
# config and asserts the daemon refuses with an actionable message.
set -euo pipefail

ROOT_DIR="${ROOT_DIR:?}"
FIXTURE_NAME="${FIXTURE_NAME:?}"
ARTIFACT_ROOT="${ARTIFACT_ROOT:?}"
# shellcheck source=/dev/null
source "$ROOT_DIR/scripts/test-docker-lib"
fixture_init

BIN="$ROOT_DIR/ductile"
SCENARIO_LOG="$ARTIFACT_DIR/scenario.log"
exec > >(tee "$SCENARIO_LOG") 2>&1

WORK="$(mktemp -d)"
WORK="$(cd "$WORK" && pwd -P)"
trap 'rm -rf "$WORK"' EXIT

# write_base <dir> — minimal valid scaffold (no api block); cases append api.yaml.
write_base() {
  local d="$1"
  mkdir -p "$d/plugins" "$d/state"
  cat > "$d/config.yaml" <<EOF
service:
  strict_mode: false
  unconfined: true
state:
  path: "./state/ductile.db"
plugin_roots:
  - "./plugins"
include:
  - api.yaml
EOF
}

# assert_refused <label> <dir> <want_msg> — `system start` must exit non-zero
# (refusal) and print want_msg. timeout 124 means it DID boot — a fail-closed miss.
assert_refused() {
  local label="$1" dir="$2" want="$3" out rc
  fixture_log "case: $label — expect refusal"
  set +e
  out=$(timeout 10 "$BIN" system start --config "$dir" 2>&1)
  rc=$?
  set -e
  printf '%s\n' "$out" > "$ARTIFACT_DIR/${label}.log"
  [[ "$rc" -ne 124 ]] || fixture_fail "$label: daemon did NOT refuse — it booted (fail-closed regression)"
  [[ "$rc" -ne 0 ]]   || fixture_fail "$label: expected non-zero exit (refusal), got 0"
  printf '%s' "$out" | grep -qi "$want" || fixture_fail "$label: refusal message missing '$want'; got: $out"
  fixture_log "$label OK — refused (rc=$rc): matched '$want'"
}

# Case 1 (#94): a literal api token is rejected — API secrets are vault-only.
C1="$WORK/literal-token"; write_base "$C1"
cat > "$C1/api.yaml" <<EOF
api:
  enabled: true
  listen: "127.0.0.1:18471"
  auth:
    tokens:
      - token: "literal-secret-not-allowed"
        scopes: ["*"]
EOF
assert_refused "literal-token" "$C1" "literal token value is not allowed"

# Case 2 (#119): API enabled with no token AND no vault to bootstrap one is refused.
C2="$WORK/enabled-no-token-no-vault"; write_base "$C2"
cat > "$C2/api.yaml" <<EOF
api:
  enabled: true
  listen: "127.0.0.1:18472"
EOF
assert_refused "enabled-no-token-no-vault" "$C2" "api.auth.tokens must be configured"

fixture_log "success — fail-closed holds: literal token and credential-less enabled API both refuse to boot"
