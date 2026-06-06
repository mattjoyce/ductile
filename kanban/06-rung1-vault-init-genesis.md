---
id: 6
status: done
priority: High
blocked_by: [2, 3]
tags: [vault, rung1, bootstrap]
---

# Rung 1 · `vault init` genesis

Bootstrap a brand-new vault.

**Scope:**
- Seed the first age blob.
- Create the reserved `core` principal (gateway) — holds the fingerprint nonce.
- Generate the fingerprint nonce.
- Mint the initial admin token (for the later authenticated management API, #8).
- **Local, key-touching only — never over the API** (see #8 / handoff coherence model).

**Acceptance:** `vault init` on an empty target produces a valid encrypted blob with `core` present and a nonce; refuses to clobber an existing vault.

## Narrative
- **Source:** handoff §"Build sequence — Rung 1" and §"Key architectural decision".
- `vault init` and `dump --values` are the only key-touching local ops that bypass the daemon API.

### Done 2026-06-01
- `internal/vault/genesis.go`: `Init(path, kr, now) (*Vault, adminToken, error)` — composition of `RegisterPrincipal` + `SetSecret` + `Save` (no new mutation logic). Seeds `core` (gateway) with a CSPRNG **fingerprint nonce** (stored, unused until Rung 4), mints a CSPRNG **admin token** stored as `core-admin-token` with **no authorized_principals** (API-internal, never Compose-delivered), persists the first blob.
- **Fail-closed:** refuses to init if the path already exists (no silent clobber of a live vault).
- `internal/secrets/token.go`: `GenerateToken(nBytes)` (crypto/rand → base64url) — canonical CSPRNG token home.
- `Principal.Nonce` (omitempty) added — only `core` carries it, per ADR §3.1.
- CLI: `ductile vault init --vault PATH --key PATH` (`cmd/ductile/vault.go`, wired into the noun switch + usage). Local/key-touching only; prints the admin token once (stdout) with store-it-now guidance (stderr). Logic stays in the library.
- Tests: library (genesis content, refuse-clobber, CSPRNG randomness, reload) + CLI (init writes blob, refuses clobber, requires flags). End-to-end smoke: armored blob mode 0600, 43-char token, re-init refused. Vault suite green; vet/lint clean; full `go test ./...` green.
