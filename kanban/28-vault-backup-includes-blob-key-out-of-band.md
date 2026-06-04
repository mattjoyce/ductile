---
id: 28
status: done
priority: Normal
blocked_by: [2]
tags: [vault, ops, backup, recovery, security]
---

# Vault · `system backup` must include vault.age (key stays out-of-band)

**Gap (found 2026-06-03 while grilling #22).** `system backup` (`cmd/ductile/system_backup.go`)
captures `configFiles = {config.yaml, api.yaml, plugins.yaml, pipelines.yaml, webhooks.yaml,
.checksums}` at `scope >= config`. **Neither `vault.age` nor the age key file is in the archive** —
the vault sits entirely outside backup. A `--scope config` restore yields a ductile install with no
secrets. Key rotation (#22) makes this bite harder: an ephemeral key means a stray vault-blob backup is
useless without its matching key.

**Trust model (decided in #22 grilling):** the archive and the key must NOT travel together, or the
archive is a single-file compromise (it already carries the `api.yaml` bearer token + env secrets).
- **`vault.age` → IN the archive** (encrypted blob; safe to include; the whole point of surviving a restore).
- **age key → OUT of the archive**, saved out-of-band by the operator (Keepass). Restore writes the key
  back by hand. This is the single-key, off-host-custody resolution of [[15-xcut-recovery-backup-story]]
  Option A's resilience WITHOUT a second recipient (the daemon stays the sole reader of the store).

**Scope:**
- Add `vault.age` (resolved vault path) to the backup plan at an appropriate scope (config or a vault
  scope), included as an opaque encrypted file.
- The manifest must tell the truth: `vault.age` included (encrypted); age key **deliberately excluded** —
  store it separately; restore requires both. Reuse the existing Excluded + Warnings manifest sections.
- Never add the age key file to the archive.
- Restore docs (`docs/OPERATOR_GUIDE.md` / `DEPLOYMENT.md` / `SECRETS.md`): vault.age + its key are a
  pair; manual key custody (Keepass); restore = unpack archive → write key file from Keepass → start.
- Document the rotation→backup discipline (cross-ref #22): after `vault rotate-key` the OLD key is
  destroyed, so any PRE-rotation vault.age backup is only restorable with the old key saved while current.

**Acceptance:** a `system backup` archive contains `vault.age`; the manifest records the key as excluded
with the pairing warning; the age key is never in the archive; restore steps documented; tests cover that
vault.age is included and the key is not.

## Narrative
- **Source:** #22 grilling (2026-06-03). Distinct from the rotate-key op (#22) — this is the backup-tool
  gap surfaced by it.
- **Relates to:** [[22-vault-recipient-rotation-coherence]] (rotation destroys old keys → pairing
  discipline), [[15-xcut-recovery-backup-story]] (strategic recovery; this is the concrete tool half;
  Option A off-host *recipient* is ruled out by "daemon is the sole reader of the store").

### Done (2026-06-03, commit `4451561`)
- **Code (`cmd/ductile/system_backup.go`):** at scope ≥ `config`, `buildBackupPlan` resolves the vault path
  (`config.ResolveVaultPath`) and, if present, adds it to the plan (`config/vault.age`) + manifest `Included`.
  It records the age key (`config.ResolveAgeKeyPath`) under manifest `Excluded` with the out-of-band reason,
  and adds two `Warnings` (pairing + rotation-destroys-old-key). `writeBackupArchive` tar-adds the blob; the
  age key file is NEVER added.
- **Trust model (per #22 grilling):** `vault.age` IN the archive (opaque encrypted file); age key OUT,
  manual custody; restore needs both. Daemon stays the sole reader — no second recipient (rules out #15
  Option A's off-host recipient).
- **Tests:** `TestBackupIncludesVaultBlobNotKey` — blob in `Included` + in the tar; key in `Excluded` with
  the out-of-band reason + NEVER in the tar; in-archive manifest tells the truth. Existing scope tests
  unchanged (they hand-build plans without the vault blob) → no regression.
- **Docs:** `SECRETS.md` §3 "Backup and restore" rewritten (was "not yet captured") with full restore steps;
  `OPERATOR_GUIDE.md` Backups + `DEPLOYMENT.md` §10 scope table cross-link it.
- Gate: gofmt/vet clean, golangci-lint 0 issues, `-race` green on cmd.
- **Note:** vault blob rides at `config` scope (alongside the other config files), not a separate `vault`
  scope — simplest and matches where `vault.age` lives.
