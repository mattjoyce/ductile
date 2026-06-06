#!/usr/bin/env bash
# secret-probe — protocol v2 test plugin for the vault-secret-delivery fixture.
# Reads the JSON request on stdin and reports a summary of the secrets the
# dispatcher composed and delivered (the `secrets` envelope): the secret NAMES
# and each value's LENGTH — never the value itself. Proves spawn-time delivery
# without leaking the secret into the job ledger.
set -euo pipefail
req="$(cat)"
summary="$(printf '%s' "$req" | jq -r '
  (.secrets // {})
  | "count=\(length) names=[\(keys|join(","))] value_lens=[\(to_entries|map("\(.key):\(.value|length)")|join(","))]"')"
printf '{"status":"ok","result":%s,"events":[],"logs":[{"level":"info","message":"secret-probe inspected delivered secrets"}]}\n' \
  "$(printf '%s' "$summary" | jq -Rs .)"
