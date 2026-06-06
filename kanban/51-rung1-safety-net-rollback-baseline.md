---
id: 51
status: done
priority: High
blocked_by: [50]
tags: [vault, deploy, thinkpad, backup, rollback]
---

# R1 — Safety net + stabilise a clean rollback baseline

Epic: [[49-epic-thinkpad-vault-field-trial]]. The current instance is RED (crash-loop). A rollback
target must be *known-good*, so we both back up AND stabilise the current binary first.

## Steps
1. **Backup (pre-trial):**
   - `ductile system backup --to ~/admin/ductile-backups/thinkpad/pre-vaulttrial-$(date -u +%Y%m%dT%H%M%SZ).tar.gz --scope plugins --config ~/.config/ductile/`
   - Also belt-and-braces the DB directly: `sqlite3 ~/.config/ductile/ductile.db "PRAGMA wal_checkpoint(TRUNCATE);"` then `cp ductile.db ductile.db.pre-vaulttrial-<stamp>`.
2. **Snapshot the current binary** for rollback: `cp ~/.local/bin/ductile ~/admin/ductile-backups/thinkpad/ductile-v0.783-68c5b08`.
3. **Stabilise current (v0.783) to GREEN** so the rollback baseline actually boots:
   - Review the 20+ manifest changes (they are expected plugin edits), then `ductile config lock --config ~/.config/ductile/`.
   - `systemctl --user restart ductile-local` and confirm it stays `active (running)`.
4. **Baseline capture (post-stabilise):** `curl -s localhost:8081/healthz`, `ductile plugin list`,
   `ductile system status --json` → save to the backup dir as `baseline-pre-trial.txt`.

## Acceptance
- A `pre-vaulttrial-*.tar.gz` (scope plugins) + a raw DB copy exist under `~/admin/ductile-backups/thinkpad/`.
- The prior binary v0.783 is saved and verified to boot GREEN (so rollback is meaningful).
- Baseline healthz/plugins/status snapshot recorded.

## Notes
- Do NOT proceed to cutover without this green baseline — the prior binary will not boot from the
  current red state without the `config lock` performed here.
