---
id: 21
status: done
priority: Normal
blocked_by: [10]
tags: [vault, lifecycle, security, bug]
---

# Vault · RevokeSecret must clear the value (revoked plaintext persists at rest)

**REAL BUG — ADR conformance (verified 2026-06-02).** `RevokeSecret` (`internal/vault/lifecycle.go:35`)
sets `Status=revoked` + `RevokedAt` but **leaves `sec.Value` intact**, so the last plaintext sits in the
(encrypted) blob after revocation. Vault ADR §3.3 says revoke should "clear value, tombstone the entry."
`roll` honours no-old-value; `revoke` does not; `Check` doesn't flag it.

**Scope:**
- Clear `Value` in `RevokeSecret` (one-line, matches the ADR).
- Optionally model the secret as a sum type `Active{value} | Revoked{revoked_at}` so valued-but-revoked
  is unrepresentable (Hickey-Armstrong §1.1). Minimum: clear the field + a test.

**Acceptance:** after revoke, the secret's stored value is empty; a `roll`-then-`revoke` leaves no
plaintext in the model; a test asserts it.

## Narrative
- **Source:** Hickey-Armstrong Branch-Review §1.1 (punch-list #3); verified against `lifecycle.go:35-46`.
- Distinct from the in-memory plaintext-lifetime concern (post-Compose, [[card 27]]) — this is at-rest
  in the blob after revoke. Low blast radius (encrypted), but a stated contract is violated; cheap fix.

### DONE (2026-06-03)
Bug-first TDD. Added `TestRevokeSecretClearsValue` (`internal/vault/lifecycle_test.go`): rolls `api`
to `"SUPERSECRET"`, revokes, asserts empty Value — confirmed RED (`got "SUPERSECRET"`). Fix: one line
`sec.Value = ""` at the active→revoked transition in `RevokeSecret` (`internal/vault/lifecycle.go:52`),
after the idempotent early-return, alongside `Status`/`RevokedAt`; mirrors `RollSecret`'s
no-retained-value contract. Invariant established: `Status==revoked ⇒ Value==""`.

Gates: `go test -race ./internal/vault/` ok; gofmt/goimports/vet clean. Security-review subagent traced
every retention/delivery path (idempotent branch, owner roll/auto-roll, RotateAdminToken, SetSecret,
Compose, AuthenticateAdmin, audit logging) → PASS: delivery branches on `Status`, never value-emptiness;
no new exposure, no audit-field leak. Sum-type rewrite deferred (Status invariant gives the same
guarantee). Not-ours dirty files left untouched.
