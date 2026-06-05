---
id: 60
status: done
priority: High
blocked_by: [53, 54, 56, 57, 58, 59, 63]
tags: [vault, deploy, thinkpad, cutover]
---

# R10 — Cutover: install the branch binary and start

Epic: [[49-epic-thinkpad-vault-field-trial]]. The single switchover step. All gates, migration,
genesis, secrets, locks, and the rollback runbook ([[63-rung13-rollback-runbook]]) must be done first.

## Steps
1. `systemctl --user stop ductile-local`.
2. Install: `cp ~/admin/ductile-backups/thinkpad/ductile-vaulttrial ~/.local/bin/ductile` (the prior
   v0.783 binary is already saved alongside for rollback).
3. `systemctl --user start ductile-local`.
4. **Verify boot**:
   - `journalctl --user -u ductile-local -n 50` shows clean start, no integrity rejection, and
     **"compose-time attestation on"** (proves the vault nonce + locks engaged).
   - `curl -s localhost:8081/healthz` → `status: ok`, `plugins_loaded` == the baseline from
     [[51-rung1-safety-net-rollback-baseline]], `plugins_circuit_open: 0`.
   - `systemctl --user status ductile-local` → `active (running)`, no auto-restart loop.

## Acceptance
- Branch binary running as `ductile-local`; boot log shows attestation on; healthz ok; plugins_loaded
  matches baseline; service stable (no crash-loop). If any check fails → invoke [[63-rung13-rollback-runbook]].
