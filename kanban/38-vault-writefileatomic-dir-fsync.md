---
id: 38
status: done
priority: Normal
blocked_by: []
tags: [vault, durability, crash-safety]
---

# Vault · writeFileAtomic must fsync the directory after rename

**Split from [[27-vault-hardening-punchlist]] (2026-06-04).** `writeFileAtomic`
(`internal/vault/vault.go` ~197-226) promises a crash-atomic write: write to a temp
file, then `rename` over the target. But it omits a **directory `fsync`** after the
rename. On ext4/XFS a rename is not guaranteed durable until the *parent directory*
is synced — so a crash right after a vault mutation can lose the rename and leave the
old ciphertext, silently reverting the operator's last set/roll/revoke. (Lamport F8.)

This is the highest-value unblocked item from #27 — a real (if latent) data-safety
gap on the sole-writer's persistence path, and a tight fix.

## Scope
- After the atomic `rename`, open the parent dir and `f.Sync()` it (then close);
  surface (not swallow) a sync error so a failed durability barrier is visible.
- Apply to the same atomic-write helper the vault Save path uses; check whether the
  config/`writeFileAtomic` in `cmd/ductile` shares the gap.
- Test: hard to simulate a crash, but assert the dir-sync code path runs (e.g. inject
  a dir-open hook, or at least a unit test that the rename target exists + content
  matches after Save). Mostly a correctness/comment fix.

**Acceptance:** the atomic vault write fsyncs the containing directory after rename;
a sync failure is returned, not ignored; existing vault Save tests stay green.

## Done (2026-06-04)
- `internal/vault/vault.go`: added `fsyncDir(dir)` (opens + `Sync()`s the dir; tolerates
  EINVAL/ENOTSUP for filesystems without dir-fsync support, surfaces real I/O errors);
  `writeFileAtomic` now `fsyncDir`s after the rename. Verified darwin `dir.Sync()` returns
  nil so the local launchd instance is unaffected; Linux performs the real barrier.
- Tests: happy-path (content + 0600 perms intact) + `fsyncDir` (real dir nil / missing errors).
  gofmt/vet/golangci-lint(0)/gosec clean; `-race` green on internal/vault.
- **Sibling gap found (FOLLOW-UP, not fixed here):** `cmd/ductile/secrets.go writeFileAtomic`
  and `config_manage.go writeFileAtomicWithBackup` have the SAME rename-without-dir-fsync gap.
  They are operator-CLI one-shots; the clean fix is a shared `internal/fsutil` atomic-write
  helper (don't duplicate fsyncDir). Worth its own small card.

## Narrative
- **Source:** 2026-06-02 Branch-Review (Lamport-Thomas-Hunt F8), via the #27 punch-list.
- **Sibling still in #27:** `RollPrincipal` partial-mutation rollback (extract a shared
  `saveOrRollback`/`mutateR[T]` helper — Lamport F6 / Ousterhout §4).
