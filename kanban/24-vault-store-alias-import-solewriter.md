---
id: 24
status: done
priority: Normal
blocked_by: [8]
tags: [vault, daemon, concurrency, sole-writer]
---

# Vault · Store() aliasing + `vault import` second-writer hole

The sole-writer guarantee (#8) has two residual escape hatches (Ousterhout §2.1/§3.3, Lamport F4):

- `Vault.Store()` (`internal/vault/vault.go:82-86`) hands out the live `*Store` pointer past the mutex
  ("use only in single-threaded contexts"). It's an unguarded alias a new caller can tear on.
- `ductile vault import` (`cmd/ductile/vault.go`) does Load → `Store()` → `Save()` directly — a lost-update
  against a running daemon (the concrete reopened hole).

**Scope:**
- Return a deep copy from `Store()` (or split the read-only load path so it never needs the aliased pointer).
- Gate `vault import` on the daemon being down, OR route it through the management API like `vault set`.
- Qualify the "sole-writer" claim comment at vault.go:16 to name these boundaries.

**Acceptance:** `Store()` cannot be used to mutate the live model past the lock; `vault import` cannot
lost-update a running daemon (refused or routed through the API); tests cover both.

## Narrative
- **Source:** Ousterhout-Liskov Branch-Review §2.1/§3.3; Lamport-Thomas-Hunt F4 + §8.2.
- **Not covered by:** #8 closed the value/management path only; `import` and the `Store()` escape hatch are
  outside it; #19's grant path uses guarded `mutate`. Latent ("a seam a new caller tears on") — `import`
  is the live instance.

### Done (2026-06-03, commit `5cfe0ad`)
- **Read alias (in-process):** added `Vault.Snapshot()` — an independent deep copy (YAML round-trip) taken
  under RLock. The load-time graft (`vaultStore`, `internal/config/vault_secrets.go`) now takes a Snapshot
  instead of the live `Store()` pointer. Test `TestSnapshotIsIndependentDeepCopy` tears the copy every way
  (nested value, new entry, principal status, grant slice) and asserts the live model is untouched.
- **Write alias (in-process):** `vault import` no longer reaches the live model through `Store().SetSecret`.
  Added guarded `Vault.SetManualBatch` — one `mutate` critical section + one Save, per-entry failures
  collected (not fatal), whole-batch rollback on Save failure. Test
  `TestSetManualBatchPersistsOnceAndReportsFailures`.
- **Second-writer (cross-process):** `vault import` is now config-driven like `vault rotate-key` — resolves
  the daemon's exact vault+key paths from `--config` and holds the daemon PID lock for the op, refusing if
  the daemon is up. Flag contract changed (`--vault/--key` → `--config`); help + import test updated; added
  `TestRunVaultImportRefusesWhileDaemonRunning` (PID lock held → refuse, vault untouched).
- **Comments:** the sole-writer claim at `vault.go:16` and the `Store()` doc now name the boundary —
  `Store()` is genesis/tests only; runtime reads via Snapshot, writes via the guarded methods, and the
  cross-process hole is closed by the PID lock.
- Gate: gofmt/vet clean, golangci-lint 0 issues, `-race` green on vault/config/cmd.
- **Deferred to [[29-vault-save-external-modification-backstop]]:** the belt-and-braces "Save fails loud if
  the blob changed underneath" backstop (catches an out-of-band writer who ignores the PID-lock discipline).
