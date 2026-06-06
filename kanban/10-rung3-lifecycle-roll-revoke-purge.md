---
id: 10
status: done
priority: Normal
blocked_by: [4, 5]
tags: [vault, rung3, lifecycle]
---

# Rung 3 · lifecycle — roll / revoke / purge

Full secret + principal lifecycle.

**Scope:**
- `roll` — values are **immutable**; a roll mints a new value from the secret's `pattern` (`auto` = vault CSPRNG, `manual` = operator-supplied) and **supersedes** the prior (old value wiped, no retained version; `roll_count` bumped). Hard cutover, single-host.
- `roll --principal` — roll **every** secret a principal can access.
- `revoke` (secret; idempotent on already-revoked), `revoke-principal`, `purge-principal` (atomic).
- Distinguish from age **recipient** rotation (`secrets rotate`, already built) — this is **value** roll.

**Acceptance:** roll produces a new value and wipes the old; `roll --principal` covers the principal's full set; revoke is idempotent; purge-principal is atomic.

## Narrative
- **Source:** handoff §"Build sequence — Rung 3"; ADR §3.3, §7 (roll semantics).
- Micro-decision: `roll` on a revoked secret → refuse (handoff §Open micro-decisions #3).
- Emits to the audit table (#11).

### Done (2026-06-02) — three slices
- **Slice 1 — pure Store ops** (`internal/vault/lifecycle.go`): `RollSecret` (supersede + bump `roll_count`, refuse on revoked), `RevokeSecret` (terminal, idempotent), `RevokePrincipal`, `PurgePrincipal` (atomic, no orphan grants). Randomness kept out of the model. Commit `55f55c7`.
- **Slice 2 — guarded owner wrappers** (`internal/vault/lifecycle_owner.go`): `Vault.Roll` (auto → CSPRNG mint, manual → operator value), `Revoke`, `RevokePrincipal`, `PurgePrincipal`, `RollPrincipal` (rolls the principal's auto secrets in one atomic Save, returns rolled + skipped-manual). Extracted a shared `Vault.mutate` (Lock → mutate → Save → rollback) and refactored `SetSecret` onto it. Race-tested.
- **Slice 3 — API + CLI**: endpoints `POST /vault/{secret,secret/roll,secret/revoke,principal/revoke,principal/purge,principal/roll}` on the vault-admin auth group (config tokens rejected); values never echoed. CLI: `ductile vault roll|revoke|revoke-principal|purge-principal|roll-principal` (keyless API clients; roll reads a manual value from stdin, never argv).
- **Decisions:** `roll --principal` rolls auto secrets and **reports skipped manual ones** (can't mint operator values) rather than failing or silently skipping; revoked secrets are not part of the live set.
- **Micro-decision honored:** roll on a revoked secret → refused (`ErrSecretRevoked`).
- Gate green across vault/api/cmd: gofmt/goimports/vet/golangci-lint(0)/gosec(0)/`-race -shuffle=on`.
- **Follow-up:** audit-table emission (#11) not yet wired; ops are ready to emit when #11 lands.
