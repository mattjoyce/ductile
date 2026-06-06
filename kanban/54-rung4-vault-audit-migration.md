---
id: 54
status: done
priority: Normal
blocked_by: [51]
tags: [vault, deploy, thinkpad, migration, sqlite]
---

# R4 — Add the `vault_audit` table (observability, not a boot gate)

Epic: [[49-epic-thinkpad-vault-field-trial]]. The branch's only new schema is `vault_audit`. It is
NOT in `requiredTables`, so the daemon boots fine without it — but the audit writer fails soft (one
Error-log per vault op). Run the migration so the audit trail is complete from day one.

## Steps
1. Confirm a backup exists ([[51-rung1-safety-net-rollback-baseline]]) — this is gated on it.
2. Copy the migration script to the host (it lives in the repo, not on the binary):
   `scp scripts/migrate-add-vault-audit-table.py matt@192.168.86.45:~/admin/scripts/` (or use the
   host checkout once on-branch).
3. Run it (idempotent, hot-safe — SQLite metadata only, `busy_timeout=5000`):
   `python3 ~/admin/scripts/migrate-add-vault-audit-table.py ~/.config/ductile/ductile.db`
   - Exit 0 + stdout `vault_audit present (with indexes) in <db>`.
4. Verify: `sqlite3 ~/.config/ductile/ductile.db ".tables" | grep vault_audit`.

## Acceptance
- `vault_audit` table + its two indexes exist on the live DB; migration is idempotent (re-run = no-op).
- No other schema migration is required by this branch (only vault_audit is new).
