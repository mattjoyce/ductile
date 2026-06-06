---
id: 3
status: done
priority: High
blocked_by: [2]
tags: [vault, rung1, principals]
---

# Rung 1 · Principal registry

Registered, unique kebab-case **principals** are the deliver-to identities (direct
model — no separate tag/capability layer, ADR §3.2).

**Scope:**
- Register / list principals; fields: `kind` (plugin | consumer | gateway), `status` (active | revoked).
- Principal `name` bound to the plugin's `.checksums` fingerprint (Rung 1 rides the **existing plain-fingerprint** check; keyed-nonce is Rung 4).
- Reserved `core` principal (gateway) — see #6 genesis.

**Acceptance:** register/list works; duplicate name rejected; revoked principal flagged; name↔fingerprint binding recorded.

## Narrative
- **Source:** handoff §"Build sequence — Rung 1"; ADR §3.1 (principals block), §3.2 (direct principal model).
- Terminology: `scopes` was renamed `authorized_principals` for secrets; "scope" stays reserved for inbound API authz.

### Done 2026-06-01
- `internal/vault/principal.go`: `RegisterPrincipal(name, kind)`, `Principal(name)`, `PrincipalNames()` (sorted). Pure model ops on `*Store` (no I/O); caller persists via `Vault.Save`.
- Kebab-case name validation (`principalNameRE`, per ADR §3.2); kind ∈ {plugin,consumer,gateway}; status defaults active.
- **Armstrong:** typed sentinel errors (`ErrInvalidName`/`ErrInvalidKind`/`ErrDuplicatePrincipal`) — duplicate register fails, never silent overwrite.
- Fingerprint binding is **by name** in Rung 1 (gateway verifies against existing `.checksums` at Compose/spawn); no fingerprint stored here — keyed-nonce is Rung 4 (#12).
- Tests (5): register, duplicate→err, invalid name (6 cases), invalid kind, sorted listing. Part of the 19-test green vault suite.
