---
id: 50
status: doing
priority: High
blocked_by: []
tags: [vault, deploy, thinkpad, recon]
---

# R0 — Deep recon of the Thinkpad instance (read-only)

Epic: [[49-epic-thinkpad-vault-field-trial]]. Understand the target before touching it.

## Findings (captured live 2026-06-05, ssh matt@192.168.86.45)
- **Host**: matt-ThinkPad-T14s-Gen-1, Ubuntu 24.04, kernel 6.17, x86_64. go1.22.2.
- **Installed**: ductile v0.783-68c5b08 (built 2026-05-27). Service `systemctl --user ductile-local`,
  api `0.0.0.0:8081`, webhooks `0.0.0.0:8091`.
- **STATE = RED**: service crash-looping. `strict_mode: true` preflight rejects 20+ plugin manifest
  hash mismatches vs `.checksums` (plugins under `~/Projects/ductile-plugins` drifted since last
  `config lock`). Not vault-related. → handled in [[51-rung1-safety-net-rollback-baseline]] / [[59-rung9-attestation-lock]].
- **Config**: `~/.config/ductile/` — config.yaml, api.yaml, tokens.yaml, plugins/*.yaml
  (discord, fabric, files, github, healthdata, identity, system, web, youtube), pipelines.yaml,
  webhooks.yaml, relay-ingress.yaml. `service.name: thinkpad`, `service.strict_mode: true`.
- **plugin_roots**: `~/Projects/ductile/plugins`, `~/Projects/ductile-plugins`, `~/Projects/ductile-healthdata`.
  48 discovered / 29 configured / 29 enabled (+ notify aliases via discord_notify).
- **environment_vars.include** (10 files): anthropic, openai, gemini, youtube, brave, discord, github,
  ductile, ollama, cerebras `.env`s — config interpolation only (see [[55-rung5-plugin-env-passthrough-audit]]).
- **Vault**: none present (no vault.age, no age key) → clean genesis.
- **DB**: ductile.db 116 MB + WAL/shm; rich `.pre-*`/`.bak` snapshot history. Backups: `~/admin/ductile-backups/thinkpad/`.
- **tokens.yaml**: 6 secrets — astro_rebuild_staging_secret, github_repo_sync_secret, git_repo_sync_secret,
  ductile_github_interest_secret, ap_canary_secret, relay-unraid-thinkpad-v1.
- **Source checkout** `~/Projects/ductile` on `main` (dirty scratch); go.mod needs 1.25 → cross-compile from Mac.

## Residual drill-down (feeds R5/R8)
- [ ] For each enabled plugin: exact env var(s) it reads (grep run.sh/manifest env usage across plugin_roots).
- [ ] tokens.yaml: which entries are literal vs `${ENV}` indirections (decides --resolve-env in R8).
- [ ] Confirm whether v0.783 currently passes full gateway env to plugins (baseline behaviour vs branch).

## Acceptance
- Instance version, config tree, plugin set, DB/schema, vault/key presence, backups, and env model
  documented (above). Residual per-plugin env + token-indirection map completed before R6 cutover.
