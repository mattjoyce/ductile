---
id: 40
status: done
priority: Normal
blocked_by: [20]
tags: [vault, security, defense-in-depth, branch-review]
---

### DONE (2026-06-05)
- `RegisterPrincipal` now refuses the reserved `core` name with `ErrReservedEntity`
  (guard runs first), reaching parity with `RevokePrincipal`/`PurgePrincipal`.
- A blanket guard would have broken genesis (which seeded `core` via `RegisterPrincipal`),
  so added the sanctioned **`SeedCorePrincipal`** path — mirroring `RotateAdminToken` for
  the reserved admin-token secret. Genesis + the fingerprint/reserved test fixtures now
  bootstrap `core` through it.
- Fixed the now-(doubly-)stale genesis docstring (closes the genesis-docstring item of
  [[47-vault-hardening-punchlist-ii]]).
- Tests: `TestReservedCorePrincipalRegisterRefused`; principal duplicate/sorted tests
  reworked off the reserved sample name. Full suite green.

# Vault · RegisterPrincipal missing reserved-principal guard

**Verified real (2026-06-04 branch code, Lamport-Thomas-Hunt N1).** `RegisterPrincipal`
(`internal/vault/principal.go:34`) does **not** call `isReservedPrincipal`, while its two sibling
mutators do (`internal/vault/lifecycle.go:61,76`). #20 added reserved-entity guards across the
data-plane mutators but missed this one.

**Reachability today is near-zero** — `core` is unpurgeable, so re-registering it isn't a live
attack — but the guard is latent and the inconsistency is a footgun. Add the
`isReservedPrincipal` check to `RegisterPrincipal` for defense-in-depth and parity with the
lifecycle mutators.

**Scope:**
- Guard `RegisterPrincipal` against reserved names (`CorePrincipal`), returning the same
  reserved-entity error the lifecycle mutators use.
- Add a test asserting `RegisterPrincipal("core", ...)` is refused.
