---
id: 20
status: done
priority: High
tags: [vault, security, bug, privesc]
---

# Vault · guard reserved core / admin-token entities against data-plane mutation

**REAL BUG — privilege escalation (verified in branch code 2026-06-02).** No mutator
guards the reserved `core` principal or the `core-admin-token` secret. Three reachable attacks
(Lamport-Thomas-Hunt Branch-Review §3.1 F1/F2/F3, corroborated Hickey-Armstrong §1.3):

- **F1 (privesc):** `SetSecret("core-admin-token", v, ["some-plugin"])` is accepted
  (`internal/vault/secret.go:26` has no reserved-name check) → the management-API admin
  credential is delivered to a plugin over stdin at next spawn. **Verified reachable** via
  `POST /vault/secret`.
- **F2:** mutating `core` / its nonce can break attestation.
- **F3:** overwriting `core-admin-token`'s value bricks the management API with no sanctioned
  recovery verb.

**Scope:**
- One `isReserved(name)` predicate in the pure model; call at the top of `SetSecret`, `RollSecret`,
  `RevokeSecret`, `RevokePrincipal`, `PurgePrincipal` → refuse reserved targets.
- Add a sanctioned `RotateAdminToken()` for the one legitimate admin-token mutation (so F3 has an exit).
- Consider folding in the `AuthenticateAdmin` early-return timing oracle (Lamport F9) while reworking
  the admin-token path.

**Acceptance:** the data plane cannot set/roll/revoke/purge `core` or `core-admin-token`; admin-token
rotation has a dedicated guarded path; a test asserts each reserved mutation is refused, and that
`core-admin-token` can never appear in any plugin's Composition.

## Narrative
- **Source:** Lamport-Thomas-Hunt Branch-Review §3.1 (F1/F2/F3, HIGH); Hickey-Armstrong Branch-Review §1.3.
  genesis.go:58 asserts "the admin token is API-internal, never composed" in a comment — not enforced.
- **Not covered by:** #10 (lifecycle preconditions enumerate revoked-terminal/orphan-grant, not
  reserved-entity immutability), #4 (set/get/check), #19 (register surface). Net-new guard.
- Single most urgent finding across all four branch reviews; ~15-line root-cause fix.

### Done (2026-06-02) — commit 894c230
- `isReservedSecret`/`isReservedPrincipal` (`internal/vault/reserved.go`); guards on `SetSecret`,
  `RollSecret`, `RevokeSecret` (admin-token secret) and `RevokePrincipal`, `PurgePrincipal` (core principal)
  → all return `ErrReservedEntity`.
- `RotateAdminToken` is the sole sanctioned write path for the admin token (forces nil
  authorized_principals + active); genesis seeds via it. Verified no other caller mutates the reserved
  entities (grep), so the guard breaks nothing.
- Tests: F1 (admin token can't be granted + never composed to a plugin), F2 (core un-revocable/un-purgeable),
  F3 (set/roll/revoke refused, value unchanged), sanctioned rotate, ordinary secrets unaffected.
- Gate: gofmt/vet clean, `-race` green on vault, full `go test ./...` no failures.
- **Follow-up:** wiring `RotateAdminToken` to an operator API/CLI verb (rotation over the mgmt API) is not
  yet done — model-layer path exists; surface it when admin-token rotation is needed operationally.
