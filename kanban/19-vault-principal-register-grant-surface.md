---
id: 19
status: done
priority: High
blocked_by: [8]
tags: [vault, api, cli, onboarding]
---

# Vault · operator surface to register principals and grant secrets

The vault can deliver secrets to a plugin, but an operator has no way to make a
plugin a principal or grant it a secret — so the #14 delivery story is currently
unreachable through the real operator path.

**Found:** 2026-06-02 hardware e2e on the Dell. The vault CLI/API exposes
`set / roll / revoke / revoke-principal / purge-principal / roll-principal`, but:
- there is **no `register-principal`** (CLI or API), and
- `vault set --principal X` calls `Store.SetSecret`, which **refuses** an
  unregistered principal (`ErrUnknownPrincipal`).

The only registered principal after genesis is `core`. To validate #14 delivery
end-to-end we had to seed a principal + grant with a throwaway Go harness
(`internal/vault` directly), because no operator path exists. `RegisterPrincipal`
exists as a pure `Store` op and is used by genesis/tests — it is simply not
wired to the management API or CLI.

**Scope:**
- `POST /vault/principal` (register: name + kind plugin|consumer|gateway) on the
  vault-admin auth group; `ductile vault register-principal --name --kind`.
- Confirm the grant path: `vault set --principal X` works once X is registered
  (it already does at the model layer; this card makes X registerable).
- Decide whether register is idempotent and whether re-register of a revoked
  principal is refused (mirror the lifecycle ops' contracts).
- Consider a `vault principals` / `vault list` read for visibility (optional).

**Acceptance:** an operator can register a plugin as a principal over the
authenticated API/CLI, then `vault set --principal <plugin> <secret>` succeeds
and the plugin receives the secret at spawn (the #14 path) — with no direct
`internal/vault` access required.

## Narrative
- **Source:** 2026-06-02 full e2e test on the Dell (192.168.20.10). Everything
  else in #8/#10/#14 worked on hardware; this is the missing operator-facing
  piece that makes plugin secret delivery actually usable.
- Aligns with the "registration = authorization only" decision (a principal is
  registered when an operator chooses to grant it secrets), settled while
  building #14.

### Done (2026-06-02)
- Guarded `Vault.RegisterPrincipal` (via the shared `mutate` helper: Lock → Save → rollback); duplicate and invalid-kind refused (delegates to `Store.RegisterPrincipal`).
- API: `POST /vault/principal` ({name, kind}) on the vault-admin auth group (config tokens rejected); `VaultManager` interface extended.
- CLI: `ductile vault register-principal --name --kind plugin|consumer|gateway` (keyless API client).
- Closes the gap: register → `vault set --principal <plugin>` → spawn delivery now works through the real operator path (no `internal/vault` harness). The Dell e2e's `vtseed` workaround is obsolete.
- Gate green across vault/api/cmd: gofmt/goimports/vet/golangci-lint(0)/gosec(0)/`-race -shuffle=on`.
- Note: register is not idempotent (duplicate → error) and there is no reactivation of a revoked principal yet — deferred unless a need surfaces.
