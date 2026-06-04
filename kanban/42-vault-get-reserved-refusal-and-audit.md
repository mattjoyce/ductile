---
id: 42
status: done
priority: Normal
blocked_by: [23, 11]
tags: [vault, cli, security, audit, branch-review]
---

### DONE (2026-06-05)
- `getSecretValue` now refuses the reserved admin-token secret (`vault.IsReservedSecret`,
  newly exported) — `vault get --name core-admin-token` no longer prints the credential.
- `vault get` emits a best-effort `vault_audit` row (`Op=read`, `Actor=cli`, name only,
  never the value): `ok` on a successful read, `denied` on the reserved-secret refusal.
- Audit is **best-effort and non-creating**: a missing state DB is skipped (no DB
  bootstrapped as a side effect of a read), and a write failure warns to stderr without
  failing the read — matching the audit fault model.
- Tests: `TestGetSecretValueRefusesReserved`, `TestAuditVaultReadNoStateDBIsNoOp`,
  `TestAuditVaultReadAppendsWhenStateDBExists`. Full suite green.

# Vault · `vault get` — add reserved-secret refusal + audit emission

**Verified real (2026-06-04 branch code, Lamport-Thomas-Hunt N3).** `vault get`
(`cmd/ductile/vault.go`) is correctly local + key-touching, but has **no reserved-secret
refusal and emits no audit row**. `ductile vault get --name core-admin-token` will print the
admin token to stdout, and the read leaves no trace in `vault_audit`.

This is inconsistent with the rest of the surface: #23 made `set` refuse the admin token (use
`rotate`), #20 guards the reserved entities, and #37 surfaces the audit log — but `get` is a
quiet side door.

**Scope:**
- Refuse `vault get` on reserved secrets (`core-admin-token`), pointing at the `rotate`/rotate-key
  path — matching the `set` refusal in #23.
- Emit a `vault_audit` row for `get` (metadata only, never the value) for least-surprise and
  RCA parity.
- Tests for both.
