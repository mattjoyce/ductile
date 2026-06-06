---
id: 5
status: done
priority: High
blocked_by: [3, 4]
tags: [vault, rung1, compose]
---

# Rung 1 · `Compose(principal) -> {secrets, denials}`

The grant realised at spawn: resolve all secrets a principal may receive.

**Scope:**
- `requires`: principal is **REGISTERED and ACTIVE** → else **fail-closed (error)**.
- `secrets` = `{ name: value | secret.status == active ∧ principal ∈ secret.authorized_principals }`.
- `denials` = typed reasons for anything withheld (secret revoked, principal not authorized, fingerprint mismatch) — a security event is a **named signal, not a silent empty map**.
- Plain in-memory lookup (store already decrypted).

**Acceptance:** active principal gets exactly its authorized active secrets; revoked secret / unauthorized principal / fingerprint mismatch each produce a typed denial; unregistered/inactive principal → hard error (fail-closed).

## Narrative
- **Source:** handoff §"Build sequence — Rung 1"; ADR §3.1 (Compose contract).
- Checks **both** secret AND principal status — the core authz invariant. This is the function dispatch will call at spawn (#14 wires delivery).

### Done 2026-06-01
- `internal/vault/compose.go`: `Compose(principal) (Composition, error)`; types `Composition{Secrets, Denials}`, `Denial{Secret, Reason}`, `DenialReason` vocab.
- **Fail-closed:** unregistered or inactive principal → hard error (`ErrUnknownPrincipal` / `ErrPrincipalInactive`), zero Composition — never an empty-but-valid result a caller could misread as "no secrets."
- Authorized + active → delivered; authorized + revoked → `Denial{secret_revoked}` (named signal, not silent drop); not-authorized → skipped (not a denial). Deterministic (secrets iterated in sorted name order).
- **Boundary (Hickey):** Compose does NOT verify fingerprints — that's the caller's concern (gateway via `.checksums` in Rung 1; keyed-nonce Rung 4). `DenialFingerprintMismatch` / `DenialPrincipalNotAuthorized` are defined as the vocabulary but emitted by caller layers, not Compose.
- Tests (6): authorized-active set, revoked→denial, unknown/inactive fail-closed, no-grants empty-not-error, deterministic denials. Vault suite now 25 green; vet/lint clean; full `go test ./...` green.
