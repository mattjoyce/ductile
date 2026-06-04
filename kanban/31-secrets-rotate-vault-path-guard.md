---
id: 31
status: done
priority: Low
blocked_by: [22]
tags: [vault, cli, secrets, sole-writer, deferred]
---

# Vault · `secrets rotate` actively refuses the vault path

**Deferred from #22 (slimming pass, 2026-06-03).** `secrets rotate` (`cmd/ductile/secrets.go`) is a
generic age-file tool that doesn't load ductile config, so it happily rewrites `vault.age` and trips the
silent-revert footgun. #22 closes this by *documentation* (rotate-key is the blessed path; never point
`secrets rotate` at the vault) and by the daemon-down PID guard on `vault rotate-key`. This card adds the
active belt-and-braces guard.

**Scope:**
- Make `secrets rotate` resolve the configured vault path (when run inside a ductile config context) and
  REFUSE if `--file` matches it: "this is the vault; use `vault rotate-key`."
- Couples a previously-generic tool to ductile config — weigh whether that's worth it vs. doc-only.

**Acceptance:** `secrets rotate --file <vault.age>` refuses with a pointer to `vault rotate-key`; a test
asserts it; non-vault files are unaffected.

## Narrative
- **Source:** #22 grilling (2026-06-03). Doc-only in #22; active guard deferred here.
- **Relates to:** [[22-vault-recipient-rotation-coherence]], [[29-vault-save-external-modification-backstop]].

## Done (2026-06-04)
- `secrets rotate` gained an optional `--config` flag and an active guard: before any
  read/decrypt, it best-effort resolves the configured vault path
  (`vaultGuardPath` → loadBackupConfig + `config.ResolveVaultPath`) and **refuses** if
  `--file` resolves to it (`samePath`: abs + EvalSymlinks + Clean), pointing to
  `ductile vault rotate-key`. Outside a ductile config context `vaultGuardPath` is ""
  so the generic age tool is unaffected — the coupling to config is best-effort, never
  a hard dependency.
- Verified: `samePath` (clean-equiv + symlink + distinct), `vaultGuardPath` (resolves for
  a config dir, "" otherwise), and a CLI test asserting `secrets rotate --file <vault>`
  exits 1 AND leaves the blob byte-for-byte unchanged. gofmt/vet/golangci-lint(0)/gosec
  clean; `-race` green on cmd/ductile.
- The doc-only half (help already says "to rotate the vault's own key use vault rotate-key")
  shipped in #22; this is the active belt-and-braces guard.
