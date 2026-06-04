---
id: 37
status: done
priority: Normal
blocked_by: [11]
tags: [cli, vault, audit, observability, consistency]
---

# CLI · surface the vault_audit log via `ductile system vault-audit`

**Surfaced 2026-06-04 during the doc audit (#35/#34).** The #35 card wanted an "audit query" and the
#34 RCA skill wanted a `vault_audit` evidence rung — but there was **no CLI** to read the table; it had
only the `Store.ListVaultAudit` reader and was queryable via raw SQL. That is inconsistent with ductile's
own pattern: every other append-only fact table (`plugin_facts`, `circuit_breaker_transitions`) is surfaced
by a read-only `ductile system <noun>` inspector. `vault_audit` was the only one missing its inspector.

Operator confirmed (2026-06-04): same fact-table pattern, so add the consistent CLI rather than document SQL.

**Scope (done):**
- Add `ductile system vault-audit [--principal NAME] [--config PATH] [--json] [--limit N]`, mirroring
  `system plugin-facts`: read-only, newest-first, human + `--json`. Shows secret NAMES + outcomes only,
  never values.
- Extend `state.ListVaultAudit(ctx, principal, limit)` with an indexed principal filter (served by the
  existing `vault_audit_principal_created_idx`) for per-principal RCA.

**Acceptance:** `system vault-audit` lists the log newest-first; `--principal` filters; `--json` emits a
structured report; empty table prints "No audit facts found"; values never appear. Tested end-to-end.

### Done (2026-06-04, commit `7d125e0`)
- `internal/state/vault_audit.go`: `ListVaultAudit` gains a principal filter (conditional WHERE, indexed).
  All call sites updated (`""` = all). New `TestListVaultAuditFiltersByPrincipal`.
- `cmd/ductile/system_state.go`: `runSystemVaultAudit` + report types + human/JSON renderers.
- `cmd/ductile/system.go` + `main.go`: wired the action, help, and the Actions/top-level help lines.
- Verified end-to-end against a seeded DB (human + `--json` + `--principal`); empty DB prints the empty note.
- Gate: gofmt/vet clean, my packages `-race` green (the dispatch `TestSpawnPluginTimeoutKillsProcessGroup`
  failure is the documented heavy-parallel flake — passes isolated; the `stopwatch_query.go` errcheck lint
  is pre-existing, untouched by this change).

## Narrative
- **Source:** doc audit (#35/#34), 2026-06-04. Consistency fix: the missing inspector for an existing table.
- **Relates to:** [[11-rung3-vault-audit-fact-table]] (the table this surfaces),
  [[34-skills-plugin-dev-and-rca-vault-coverage]] + [[35-docs-vault-attestation-coverage]] (docs that now
  reference this command instead of raw SQL).
