---
id: 29
status: done
priority: Normal
blocked_by: [8]
tags: [vault, daemon, sole-writer, security, defense-in-depth]
---

# Vault · Save fails loud if the blob changed underneath the daemon

**Deferred from #22 (slimming pass, 2026-06-03).** Defense-in-depth backstop: the running daemon's
`Save` (`internal/vault/vault.go`) should remember the ciphertext it last wrote/loaded and, before
overwriting `vault.age`, re-read the file and **fail loud** (`ErrVaultModifiedExternally`) if the bytes
changed underneath it — instead of silently clobbering. Catches ANY out-of-band writer (a stray
`secrets rotate`, a manual edit, a botched restore), not just the ones we remember to guard.

Not required for #22's core once `vault rotate-key` is the blessed, daemon-down, PID-guarded path — this
only guards against an operator ignoring the docs and writing the live blob out-of-band.

**Scope:**
- `Save`/`Load` remember the last-written/loaded ciphertext hash.
- Pre-overwrite: re-read `vault.age`, compare hash; mismatch → `ErrVaultModifiedExternally`, don't write,
  escalate (log + operator told to restart/reload to adopt the on-disk blob).
- Known residual: sub-ms TOCTOU between re-read and rename — acceptable for the operator-footgun threat.

**Acceptance:** with the daemon running, an out-of-band modification of `vault.age` makes the next `Save`
refuse loudly rather than silently revert; a test asserts it.

**Adjacent durability nit (from #22 security review, 2026-06-03):** `vault.RotateKey` verifies the new
key decrypts the new blob (Phase 2) but does not re-read the *persisted* key file after writing it (Phase 3)
before narrowing the blob to {new} (Phase 4). If the rename succeeded but the on-disk key were corrupt
(extremely unlikely given fsync+rename), next boot would brick. Cheap hardening: reload the written key file
and confirm it decrypts the bridge blob before retiring the old recipient. Low priority; fold in here.

## Narrative
- **Source:** #22 grilling (2026-06-03), "leg 2 / owner defends its file" — deferred to keep #22 slim.
- **Relates to:** [[22-vault-recipient-rotation-coherence]], [[08-arch-daemon-sole-writer-api]].

### Done (2026-06-03, commit `3c01c58`)
- **Backstop:** `Vault` now remembers `lastDiskHash` (SHA-256 of the ciphertext last written/loaded; set
  in `Load` and after each `Save`). `Save` calls `checkDiskUnchanged()` before a real write: re-reads
  `vault.age`, and on a hash mismatch returns `ErrVaultModifiedExternally` (typed; `errors.Is`-able) without
  writing. A nil baseline (genesis / fresh `New`) and a missing file are allowed (the daemon re-asserts its
  own model). No-op Saves skip the check (they overwrite nothing). Residual sub-ms TOCTOU before rename is
  the accepted footgun-threat boundary, as the card specified.
- **mutate() interaction:** an external-mod error propagates through `mutate`, which rolls the in-memory
  mutation back to `lastYAML` — so memory stays consistent with the daemon's last-known write and the
  operator is told to restart/reload to adopt the on-disk blob. Subsequent Saves keep failing until then
  (fail-loud-and-stay-failed).
- **Adjacent #22 durability nit (folded in):** `RotateKey` now STAGES the new key beside the boot path,
  proves the PERSISTED bytes (not just the in-memory mint) decrypt the on-disk blob
  (`verifyKeyFileDecrypts`), and only then atomically promotes it and retires the old recipient. A corrupt
  persist aborts with the boot key still the OLD key and the blob still `{old,new}` — no rollback needed, no
  brick. `RotateKey` also rebases `lastDiskHash` on the final blob so a post-rotation `Save` is not mistaken
  for an external write.
- **Tests:** `TestSaveRefusesExternalModification` (acceptance — refuse + on-disk blob untouched + refused
  change not leaked), `TestSaveAfterRotateKeyPersists` (no false-trigger after rotation). Existing rotate-key
  + round-trip + mutate tests still green.
- Gate: gofmt/vet clean, golangci-lint 0 issues, gosec clean, `-race -shuffle` green on vault + cmd.
