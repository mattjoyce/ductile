---
id: 32
status: done
priority: Normal
blocked_by: [8]
tags: [docs, cli, help, vault, branch-sweep]
---

# Docs · CLI `--help` coverage for the vault/secrets/attestation surface

**From the branch doc-sweep (2026-06-03).** The `feat/age-secrets-and-spawn-hygiene` branch shipped a large
CLI surface that the `--help`/usage printers in `cmd/ductile/` don't fully reflect — several commands are
undiscoverable from top-level help.

**Gaps (cmd/ductile help printers):**
- **`main.go` printUsage (HIGH):** no "Vault Commands" section — all 10 `vault` subcommands (init, import,
  set, register-principal, roll, revoke, revoke-principal, purge-principal, roll-principal, rotate-key) are
  undiscoverable. No "Secrets Commands" detail (keygen/encrypt/rotate). Missing `system backup`, `system
  breaker`, `system selfcheck` from the System list.
- **`system_backup.go` help (HIGH):** lists the config-scope files but never states whether `vault.age` is
  included (it is NOT — see #28); operators can't tell if secrets are backed up.
- **`vault.go` printVaultNounHelp (MED):** subcommands listed but per-command flags aren't (`--vault/--key`
  for init/import, `--api-url/--token/--name/--pattern/--principal` for set, `--config` for rotate-key, …);
  no distinction between **keyless API clients** (set/roll/revoke/…) and **local key-touching** ops
  (init/import/rotate-key, daemon-down).
- **`secrets.go` help (MED):** `secrets rotate` description doesn't warn it's for config bundles, NOT the
  vault — conflatable with `vault roll`/`vault rotate-key`.
- **`plugin_lock.go` help (MED):** the "requires a loadable vault / keyed by the vault nonce" prerequisite
  is buried.
- **`config.go` (LOW):** `config lock` help lacks the decoupling context (no longer touches plugin
  attestation — that's `plugin lock`); no per-action help for config init/backup/restore.

**Acceptance:** `ductile --help` discovers the vault + secrets nouns and all system actions; vault noun
help documents per-command flags + the keyless/key-touching split; secrets-rotate warns it is not for the
vault; backup help states vault.age coverage; plugin-lock prerequisite is prominent.

## Narrative
- **Source:** branch doc-sweep, `--help` explorer (2026-06-03).
- **Relates to:** [[28-vault-backup-includes-blob-key-out-of-band]] (backup help), [[22-vault-recipient-rotation-coherence]] (rotate-key), and the attestation cards.

### DONE (2026-06-03)
Verified the real gaps against code first (the sweep over-claimed: `secrets`, `plugin lock`, and `config
lock` decoupling were already in printUsage). Fixed:
- `main.go printUsage`: added a **Vault Commands** section (10 subcommands, tagged local-key-touching vs
  keyless-API-client); added missing System actions `breaker`, `selfcheck`, `backup`.
- `vault.go printVaultNounHelp`: regrouped into the two write classes with per-command flags and a pointer
  to `vault <action> --help`.
- `secrets.go`: `secrets` help now states these operate on config bundles, NOT the vault → use `vault
  rotate-key`.
- `system_backup.go`: help notes `vault.age` + key are NOT in any scope (back up separately; see #28).
Verified by building and rendering `ductile help` / `vault help` / `secrets help`. gofmt+vet clean.
Skipped LOW items (per-action config init/backup/restore help printers; plugin_lock wording already names
the vault prerequisite) — not worth the churn.
