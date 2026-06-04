---
id: 9
status: done
priority: Normal
blocked_by: [4, 5]
tags: [vault, rung2, migration, references]
---

# Rung 2 · migration & `secret_ref:` references

Wire the vault into config consumption and migrate off `${ENV}`.

**Scope:**
- Generalise the existing **`secret_ref:`** field (today webhooks/relay only) so any consumer resolves a secret by name from the vault.
- Import existing `tokens.yaml` entries into the vault (literal values move in; `key: ${ENV}` entries import the resolved value on operator confirmation or are flagged).
- **`${ENV}` coexistence window** during migration; `secret_ref:` resolves via the vault.
- Invariant target: **"if it is a secret, it is in the vault."** `tokens.yaml` becomes a back-compat shim, removed in a few deploys; the non-secret inbound `scopes_file` binding re-homes to its own auth config.

**Acceptance:** a plugin references a vault secret via `secret_ref:` and receives the value; a `tokens.yaml` entry imports cleanly; `${ENV}` still resolves during the window.

## Narrative
- **Source:** handoff §"Build sequence — Rung 2"; ADR §3.4 (`secret_ref:` decided), §6/§7 (replace-vs-coexist invariant). Design grounded in a full re-read of the vault ADR + four design reviews; naming settled (secret = class, token = inbound API credential — ADR glossary updated).
- **Approach (resolved with operator):** an *adapter* — graft vault secrets into the legacy `cfg.Tokens` resolution map at load — rather than rewriting every consumer. Reuses the `graftTokens` pattern; zero resolver changes. Serves the **load-time** consumer class (webhook/relay); plugin spawn-time delivery via `Compose` is #14.

### Done 2026-06-01
Shipped as four vertical slices, TDD; full `go test ./...` green (0 failures).
- **Resolution** — `internal/config/vault_secrets.go`: `SecretsConfig.VaultFile` (default `<configDir>/vault.age`); `graftVaultSecrets` loads the vault after the keyring resolves and overlays active secrets onto `cfg.Tokens` before validation. Pure `mergeVaultSecrets` (vault-wins + collision warning) split from I/O. Degradations: no vault → no-op (coexistence); keyless → no-op (daemon's job, ADR §3.5.1); present-but-broken → fail-closed. Revoked secrets excluded.
- **Import** — `ductile vault import` (`cmd/ductile/vault.go`) + pure `config.PlanTokenImport`: literal values migrate in; `${ENV}` pointers flagged unless `--resolve-env` resolves them; local key-touching op (never over the API), like `init`.
- **Legacy-mutator guard** — `loadTokensFile`/`writeTokensFile` refuse + redirect to the vault on an age-encrypted tokens file (ADR §7/§8); verified no clobber, no `.bak`.
- **Keyless-validate softening** — `ConfigValidator.vaultBlind`: a `secret_ref` the keyless validator cannot resolve becomes a warning, not an error (vault-only secrets are the daemon's to validate — resolves the Ousterhout §4 whole-store-encryption vs static-validate fork). Four existence checks consolidated into one `checkSecretRef`.
- **Precedence decision** (was a flagged-open review item): vault wins over a same-named `tokens.yaml` entry, with a warning to remove the dupe.
- **Deferred to shim removal:** the `cfg.Tokens → resolvedSecrets` rename (kept the legacy name, doc-flagged, during coexistence).

### SUNSET NOTE (2026-06-05)
`ductile vault import` is **transitional**, not standing surface: it's a one-way ratchet onto the
vault and has no reason to exist once `tokens.yaml` is gone. It (and the graft) are demolished by
[[48-epic-retire-tokens-yaml]] slice 3. Don't invest in `import` as permanent CLI (no deep
doc/skill coverage); treat it as scaffolding marked for removal.
