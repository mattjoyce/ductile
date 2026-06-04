---
id: 4
status: done
priority: High
blocked_by: [2]
tags: [vault, rung1, secrets]
---

# Rung 1 · Secret model + `set` / `get` / `check`

The secret entry and the basic local operations on it.

**Scope:**
- Entry fields: `value`, `authorized_principals[]`, `status` (active | revoked), `pattern` (auto | manual), `roll_count`, `created_at`/`updated_at`/`revoked_at`/`description`.
- `set` — register/update a secret (value held in vault; never written un-encrypted).
- `get` — retrieve one secret's value (local, key-touching).
- `check` — integrity/health: store decrypts, entries parse, no orphan principals; machine-readable.

**Acceptance:** `set` then `get` roundtrips; `check` reports a healthy store and flags an orphaned `authorized_principals` reference.

## Narrative
- **Source:** handoff §"Build sequence — Rung 1"; ADR §3.1 (secrets block), §3.3 (lifecycle).
- Micro-decision (decide here): lifecycle preconditions — `set` on a tombstoned name (handoff §Open micro-decisions #3).
- `value` is immutable; changing it is a **roll** (Rung 3, #10), not a `set`.

### Done 2026-06-01
- `internal/vault/secret.go`: `SetSecret(name,value,authorized,pattern,now)` (upsert), `Secret(name)`, `SecretNames()`, `Check() []Issue`.
- **Hickey:** pure ops on `*Store`; `now` injected (model is clock-free/testable); defensive copy of `authorized_principals` so caller mutation can't reach the model.
- **Armstrong / fail-closed:** name + pattern validated; **every authorized principal must already be registered** (orphan grant refused *before* mutation, not just flagged later); updating a **revoked** secret refused (terminal); typed sentinels (`ErrUnknownPrincipal`/`ErrInvalidPattern`/`ErrSecretRevoked`/…).
- `Check()` = structured `[]Issue` (machine-readable): enum sanity + orphaned grants; deterministic (sorted); pure, no I/O.
- Micro-decision settled: set-on-tombstoned(revoked) → refuse; set-on-active → update (keeps `created_at`, bumps `updated_at`).
- Tests (7): create (timestamps), update (created stable/updated bumped), unknown-principal fail-closed, revoked fail, invalid name+pattern, defensive-copy, Check healthy+orphan. Part of the 19-test green vault suite.
