---
id: 53
status: done
priority: Normal
blocked_by: [51, 52]
tags: [vault, deploy, thinkpad, selfcheck, gate]
---

# R3 — Offline deploy-gate (new binary, service stopped)

Epic: [[49-epic-thinkpad-vault-field-trial]]. The canonical pre-cutover gate: run the NEW binary's
integrity checks OFFLINE (service stopped) so schema/config problems surface before cutover, not after.

## Steps
1. Stop the service: `systemctl --user stop ductile-local` (release the PID lock; checks 4–6 only run offline).
2. Schema/integrity against the live DB with the new binary:
   `~/admin/ductile-backups/thinkpad/ductile-vaulttrial system selfcheck --json --config ~/.config/ductile/`
   - Expect a real (non-skipped) `db_schema` result. vault_audit is NOT required, so a missing
     vault_audit table will NOT fail this gate (handled in [[54-rung4-vault-audit-migration]]).
3. Config validation: `… config check --config ~/.config/ductile/` against the CURRENT config
   (pre-reconciliation). Note expected failures/warnings (strict_mode alias, missing secrets.*).
4. Record both outputs to the backup dir as `deploy-gate-pre-reconcile.txt`.

## Acceptance
- New binary's `selfcheck` confirms DB schema is valid (or names exactly what's missing).
- `config check` output captured; any required config changes feed [[56-rung6-config-reconciliation]].
- Service can be restarted on the OLD binary afterwards if we pause here (no irreversible step taken).
