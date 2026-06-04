---
id: 22
status: done
priority: Normal
blocked_by: [8]
tags: [vault, daemon, recipients, security, bug]
---

# Vault · recipient rotation silently reverted under a running daemon

**REAL BUG (branch-review 2026-06-02).** `secrets rotate` (`cmd/ductile/secrets.go:124-179`) rewrites
`vault.age` as a standalone file op, but the daemon captured its `Keyring` at boot (`runtime.go:567`) and
holds the OLD identities/recipients. The next daemon mutation's `Save` (`vault.go:182`) re-encrypts the
blob to the **old** recipient set, silently undoing the rotation; only SIGHUP rebuilds the keyring. A
security operation (key rotation) silently no-ops against a live daemon — same sole-writer root as #8, but
for the recipient path.

**Scope:**
- Route rotation through the daemon (like `vault set`), OR have the daemon reload its keyring before the
  next `Save` when the blob/recipients changed on disk.
- At minimum: make `Save` fail loudly if its in-memory recipients no longer match the on-disk age header,
  and document that rotation requires a daemon restart/reload.

**Acceptance:** a recipient rotation performed while the daemon runs is not silently reverted by the next
mutation — either it takes effect, or it fails loudly and the operator is told to reload.

## Narrative
- **Source:** Hickey-Armstrong Branch-Review §2.2 (punch-list #2). Cites secrets.go:124-179, resolveKeyring
  (secrets.go:25), vault.go:182, keyring.go:38-51, runtime.go:567.
- **Not covered by:** #8 closed the *value/management* sole-writer path only; #10 distinguishes value-roll
  from `secrets rotate` but never addresses the rotate path's daemon coherence.

### DONE (2026-06-03)
Grilled (grill-with-docs) → reframed. The store is the daemon's PRIVATE blob and the daemon is its sole
reader, so recipients stay derived (no recipient-as-data) and "rotate recipients" ≡ "rotate the daemon's
key." Per #8 (key-touching ops are local, like `vault init`), shipped a LOCAL daemon-down command rather
than a live API op.

**`ductile vault rotate-key`** (`cmd/ductile/vault.go`): resolves the daemon's exact vault+key paths from
config (`config.ResolveVaultPath`/`ResolveAgeKeyPath` — boot coherence), guards on the daemon being down
via the existing PID lock (refuses if held), then delegates to **`vault.RotateKey`** (`internal/vault/
rotate_key.go`). Safety = dual-recipient bridge + verify-before-retire under the write lock: mint → encrypt
to {old,new} → verify the new key alone decrypts the new blob → atomic-write new key to the boot path (0600,
no .bak) → re-encrypt to {new} → adopt the new keyring (`secrets.NewKeyring`). Every crash-state keeps a
coherent (key,blob) pair. New key surfaced for out-of-band (Keepass) backup.

Tests (red→green, `-race`): vault-level (old-key-fails/new-key-works/model-preserved/0600, keyring-adopt,
verify-gate) + CLI (daemon-down rotate, daemon-running refusal leaves vault untouched). Gates green:
gofmt/goimports/vet/golangci-lint(0)/gosec(0). /simplify: dropped unused param, de-duped key-path
precedence, slices.Concat. Docs → OPERATOR_GUIDE (not the not-ours SECRETS.md).

**Slimmed (YAGNI, operator call) → new cards:** live/API rotation #30 · Save external-modification
backstop #29 · active `secrets rotate` vault guard #31 · `system backup` includes vault.age #28.
Acceptance met: rotation TAKES EFFECT (daemon down) or FAILS LOUD (PID-lock refusal while up).
