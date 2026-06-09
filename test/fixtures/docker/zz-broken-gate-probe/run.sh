#!/usr/bin/env bash
# TEMPORARY #116 gate probe — deliberately red. Proves a broken fixture turns
# docker-validation red. Reverted immediately after the CI evidence run.
set -euo pipefail
echo "[fixture:zz-broken-gate-probe] deliberately failing to prove the gate bites"
exit 1
