---
id: 8
status: done
priority: High
blocked_by: [2]
tags: [vault, architecture, api]
---

# Architecture · daemon sole-writer + authenticated management API

Coherence/auth model that shapes how management ops are exposed (beyond the Rung 1 library).

**Decision (settled):**
- The **daemon** is the sole holder of the key and the in-memory model → the **sole writer**.
- Management ops (`set` / `roll` / `revoke` / …) go through the daemon's **authenticated API** (Bearer token + scopes), mirroring `ductile system reload` (`cmd/ductile/system_state.go:518`).
- `dump --values` and `vault init` are **local, key-touching only — never over the API**.
- The CLI is an **API client that holds no key and decrypts nothing**.
- Consequences: B→C (daemon split) becomes a deployment change, not a re-contract; metadata-dependent validation routes through the daemon API (settles the PrivSec §11 contradiction).

**Acceptance:** management mutations require a valid Bearer token + scope; CLI performs no decryption; key-touching ops refuse to run over the API.

## Narrative
- **Source:** handoff §"Key architectural decision — coherence / auth model".
- Implemented as ops get exposed (Rung 2/3); Rung 1 builds the library callable locally + `vault init`.

### Done (2026-06-02)
Three forks settled in chat (registration = authorization only; auth = vault-resident token; owner = RWMutex on Vault), then TDD in three slices.
- **Slice A — guarded sole-writer owner:** `*vault.Vault` carries a `sync.RWMutex`; `Compose` runs under `RLock`, `SetSecret` under `Lock` + atomic `Save`, rolling the in-memory model back from `lastYAML` if persistence fails. `Store` stays the pure, lock-free model. `config.LoadVault` hands the runtime one owner (`loadVaultOwner` shared with #9's graft); dispatch reads now go through guarded `Compose`. Race-tested.
- **Slice B — authenticated management API:** `vault.AuthenticateAdmin` constant-time-checks the resident `core-admin-token` (Fork 1a — config tokens, even wildcard, are rejected on vault routes). `api.VaultManager` interface + `authenticateVaultAdmin` middleware + `POST /vault/secret` in its own auth group; value never echoed. Runtime wires the owner in (typed-nil guarded).
- **Slice C — keyless CLI client:** `ductile vault set` POSTs to `/vault/secret` with the admin token (`--token`/`DUCTILE_VAULT_TOKEN`); the secret value is read from **stdin, never argv**. Holds no key, decrypts nothing.
- **Acceptance met:** mutations require a valid Bearer (vault admin token); `init`/`import`/dump stay local key-touching, never over the API; the CLI is a keyless client.
- Commits: `ea75bea` (slices A+B), slice C following. Gate green: gofmt/goimports/vet/golangci-lint(0)/gosec(0)/`-race -shuffle=on` across vault/api/config/cmd.
- **Follow-ups:** roll/revoke/purge ops (#10) extend the same API surface; #18 (graft tightening) still pending.
