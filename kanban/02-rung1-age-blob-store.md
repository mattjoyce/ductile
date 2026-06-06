---
id: 2
status: done
priority: High
tags: [vault, rung1, storage]
---

# Rung 1 · `internal/vault` age-blob store

Create the `internal/vault` module: the persistence floor for the vault.

**Scope:**
- In-memory YAML model of the store (secrets, principals, grants, values), resident for the gateway's lifetime.
- **Decrypt-on-load:** read the single age blob → decrypt → parse YAML → model.
- **Persist-on-change:** mutate model → serialise YAML → age-encrypt the *whole* document → **atomic write, no `.bak`**; write back **only when values change**.
- Metadata hidden at rest (whole-store blob, not per-value encryption — see ADR §3.1; SQLite/per-value detour was reversed).
- Reuse existing `internal/secrets` (age) primitives.

**Acceptance:** roundtrip (model → encrypt → decrypt → identical model); atomic write leaves no partial/`.bak` file; no write when nothing changed.

## Narrative
- **Source:** handoff §"Build sequence — Rung 1"; ADR `Ductile - Vault.md` §3.1 (in-memory model, whole-store age blob).
- Micro-decision (decide here): **crash/persist ordering — persist blob, *then* swap the in-memory snapshot on success** (handoff §Open micro-decisions #2).
- Prereqs present on branch: `internal/secrets/age.go`, `internal/secrets/keyring.go`.

### Done 2026-06-01
- Built `internal/vault/`: `store.go` (pure data — `Store`/`Secret`/`Principal` + enum consts + `NewStore`) and `vault.go` (process — `New`/`Load`/`Save`/`Store`/`Path` + `writeFileAtomic`).
- **Hickey:** data (the model, no I/O) split from process (the vault, all I/O); state has one home (blob at rest, one resident model owned by `Vault`, no globals); encryption stays in `internal/secrets`, model never knows it's encrypted.
- **Armstrong:** fail-closed `Load` (missing / not-encrypted / wrong-key / malformed all error — never a silent empty store); atomic write = temp→chmod 0600→**fsync**→rename, **no `.bak`**; write-on-change compares canonical *plaintext* YAML (age is non-deterministic, so ciphertext compare wouldn't work).
- Added `Keyring.Recipients()` (derive recipients from X25519 identities) so the vault self-encrypts on save.
- **Decisions:** crash/persist order = persist blob then update in-memory baseline on success (micro-decision #2); write-on-change baseline canonicalised on Load so a no-op Save after Load doesn't rewrite; off-host backup recipients deferred to #15 (single-host self-encryption now, YAGNI).
- Tests (7, all green): empty + populated round-trip; missing/plaintext/wrong-key fail-closed; encrypted-blob-no-leak incl. **metadata (secret name) hidden at rest** + no temp/`.bak` left; write-on-change via byte-identity + persistence across reload. gofmt/vet/golangci-lint clean; full `go test ./...` green.
- **Uncommitted** on top of checkpoint `27b4849` (per "build on top, don't commit each step").
