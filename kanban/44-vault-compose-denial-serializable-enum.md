---
id: 44
status: wontfix
priority: Normal
blocked_by: [13]
tags: [vault, compose, rpc, daemon, wontfix]
---

### WON'T FIX (2026-06-05) — dependency killed
This card existed only so compose denial reasons would survive an RPC hop to a `vaultd`
daemon. The operator has made [[13-rung5-vaultd-daemon]] a hard no, so that hop will never
exist. In-process `errors.Is` on `ErrUnknownPrincipal` / `ErrFingerprintMismatch`
(`secret_delivery.go`, `compose_escalation.go`) is correct and idiomatic permanently. No work
to do.

# Vault · compose denial reasons → serializable enum (RPC-portable)

**From the 2026-06-04 branch review (Ousterhout-Liskov §3).** Two control flows hinge on Go
**error identity** — `errors.Is(..., ErrUnknownPrincipal)` and `errors.Is(..., ErrFingerprintMismatch)`
(`secret_delivery.go:61,64`, `compose_escalation.go:18`). That coupling won't survive an RPC hop
to a daemon: error identity doesn't serialize, so both the benign opt-out path and the security
escalation would break the moment compose runs across a process boundary (the form-C `vaultd`
direction, #13).

**Scope (do before any A→B→C daemon swap):**
- Have the composer contract return a typed, serializable **reason enum**
  (e.g. `not_a_principal | revoked | fingerprint_mismatch`) alongside/instead of sentinel errors.
- Migrate the `errors.Is` call sites to switch on the enum, preserving both the benign-denial and
  security-escalation (#25) branches.
- Test that escalation vs. benign denial is distinguished via the enum, not error identity.
