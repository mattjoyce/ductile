---
id: 63
status: done
priority: High
blocked_by: [51]
tags: [vault, deploy, thinkpad, rollback, runbook]
---

# R13 — Rollback runbook (written + dry-tested before cutover)

Epic: [[49-epic-thinkpad-vault-field-trial]]. A precondition of cutover ([[60-rung10-cutover]]):
a clear, tested way back to the green v0.783 baseline from [[51-rung1-safety-net-rollback-baseline]].

## Procedure
1. `systemctl --user stop ductile-local`.
2. Restore the prior binary: `cp ~/admin/ductile-backups/thinkpad/ductile-v0.783-68c5b08 ~/.local/bin/ductile`.
3. **DB**: the only branch schema change is the additive `vault_audit` table — harmless to the old
   binary (not in its requiredTables), so **no DB restore is normally needed**. Restore
   `ductile.db.pre-vaulttrial-<stamp>` ONLY if the DB was otherwise mutated/corrupted.
4. **Config**: revert config.yaml to `strict_mode: true` (or git-restore the config snapshot) if the
   admission-gate edits cause the old binary to misbehave. `vault.age`/age key are inert to v0.783.
5. `systemctl --user start ductile-local`; confirm GREEN baseline (healthz ok, plugins_loaded == baseline).

## Notes / caveats
- The old binary needs the green `.checksums` from R1's `config lock` — do not skip R1's stabilise step.
- Keep `vault.age` + age key during rollback (no need to delete); they don't affect v0.783.

## Concrete artifacts from the 2026-06-05 cutover (stamp 20260604T225434Z)
All under `~/admin/ductile-backups/thinkpad/`:
- Prior binary: `ductile-v0.783-68c5b08`  → `cp` to `~/.local/bin/ductile` to roll back.
- Full backup: `pre-vaulttrial-20260604T225434Z.tar.gz` (db + config).
- Raw DB: `ductile.db.pre-vaulttrial-20260604T225434Z`.
Config snapshots in `~/.config/ductile/`:
- `config.yaml.pre-vaulttrial-20260604T225434Z` (old, strict_mode:true — restore for the old binary).
- `.checksums.pre-vaulttrial-20260604T225434Z` (old-format checksums — restore so v0.783 integrity passes).

### Rollback sequence (verified artifacts present)
1. `systemctl --user stop ductile-local`
2. `cp ~/admin/ductile-backups/thinkpad/ductile-v0.783-68c5b08 ~/.local/bin/ductile`
3. `cp ~/.config/ductile/config.yaml.pre-vaulttrial-* ~/.config/ductile/config.yaml`
4. `cp ~/.config/ductile/.checksums.pre-vaulttrial-* ~/.config/ductile/.checksums`
5. (DB restore only if mutated: `cp ...ductile.db.pre-vaulttrial-* ~/.config/ductile/ductile.db`) — vault_audit
   table is additive/harmless to v0.783, so normally NOT needed.
6. `systemctl --user start ductile-local` → confirm green (healthz ok, plugins_loaded 48).
- `vault.age` + `~/.config/secrets/ductile/age.key` are inert to v0.783 — leave them in place.

## Acceptance
- Written runbook exists; each step dry-validated (paths/files confirmed present) BEFORE cutover.
