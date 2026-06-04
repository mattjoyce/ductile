---
id: 18
status: done
priority: Normal
blocked_by: [14]
tags: [vault, dispatch, config, security]
---

# Cross-cutting · tighten #9's load-time graft to non-plugin scope

Once #14 delivers plugin secrets per-principal at spawn (`Compose`→stdin), the
load-time graft must stop exposing plugin-scoped secrets gateway-globally.

**Background:** #9 grafts *all* active vault secrets into the load-time
`cfg.Tokens` map (`internal/config/vault_secrets.go: activeVaultSecrets`). That
was correct while no spawn-time delivery existed. #14 introduced
`Compose(plugin)`→stdin delivery, so a plugin's secrets now reach it directly —
leaving them in the global graft means they stay visible to *every* gateway/
load-time consumer (webhook/relay HMAC resolution).

**Scope:**
- Restrict the load-time graft to secrets that gateway/consumer-side consumers
  legitimately resolve via `secret_ref`, excluding exclusively-plugin-scoped
  secrets.
- Decision still open (see this session's grill): the candidate rule is "drop a
  secret whose `AuthorizedPrincipals` is non-empty and entirely `KindPlugin`",
  keeping gateway/consumer-authorized and unscoped (migrated tokens.yaml)
  secrets. Settle against the ADR before coding.
- Touch points: `activeVaultSecrets` / `mergeVaultSecrets`; the principal
  registry (`KindPlugin/KindConsumer/KindGateway`) provides the scope signal.

**Acceptance:** an exclusively-plugin-scoped secret is delivered to its plugin
via `Compose` at spawn but is NOT present in the load-time `cfg.Tokens` graft; a
gateway/consumer-authorized or unscoped (migrated) secret still resolves via
`secret_ref` as today.

## Narrative
- **Source:** split out of #14's "MUST tighten #9's graft" per the 2026-06-02
  session — slice 1 (Compose→stdin delivery) landed first; the graft-scope
  change is ADR-adjacent and deferred to its own card.
- Depends on #14 (per-plugin delivery must exist before plugin secrets can be
  removed from the global graft).

### Done (2026-06-02)
- **Rule chosen:** drop a secret from the load-time graft iff its
  `AuthorizedPrincipals` is non-empty AND every principal resolves to a
  registered `KindPlugin` principal. Unscoped (migrated tokens.yaml) and
  gateway/consumer-authorized secrets stay; an orphan/unknown-principal grant
  keeps the secret gateway-visible (fail toward the load-time consumer).
- **Where:** a pure `pluginScopedSecret` filter inside `activeVaultSecrets`
  (`internal/config/vault_secrets.go`) — the audience/projection decision is a
  config-layer concern, so the vault model stays pure. ~25 lines.
- Plugin-scoped secrets now reach plugins ONLY via spawn-time `Compose` (#14);
  `secret_ref` resolution is unchanged for gateway/consumer/unscoped secrets.
- Updated the #9 round-trip test to grant to a consumer principal (the graft's
  real audience); added `TestActiveVaultSecretsExcludesPluginScoped`.
- Gate green: gofmt/vet/golangci-lint(0)/gosec(0)/`-race -shuffle=on` on config.
