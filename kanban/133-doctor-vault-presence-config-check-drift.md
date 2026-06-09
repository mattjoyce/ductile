---
id: 133
status: backlog
priority: High
tags: [doctor, config, vault, posture, bootstrap, cli]
---

# Doctor's vault-presence input drifts: `config check` rejects the bootstrap config the daemon boots

> Raised 2026-06-10, dual luminary code review of `feat/129-vault-operable-posture`
> (Lamport×Thomas/Hunt F1 **and** Hickey×Armstrong F1 — found independently by both reviewers,
> empirically confirmed on a temp dir).

## The concern
`APIEnabledWithoutToken(cfg, hasVault)` is single-sourced and vault-aware in load-validation
(`internal/config/loader.go:166-168`) and boot admission (`cmd/ductile/runtime.go:457,636`) — but
`Doctor.vaultPresent` defaults **false** and only the boot path calls `WithVaultPresent`.
`runConfigCheck` (`cmd/ductile/config.go:679`) and `validateConfigAtPath`
(`cmd/ductile/config_manage.go:75-108` — behind `config set`/`plugin set`/route/webhook
mutations/`init`/`restore` and `/system/doctor`) stay vault-blind, even though the `config.Load`
they each call **already decrypted the vault**.

Confirmed: genesis vault + zero-token api block → `ductile config check` exits 1 with
"api.auth.tokens must be configured … (or genesis a vault to bootstrap one)" — while the vault it
suggests sits beside the config — and `system start` boots management posture on the identical dir.
`docs/DEPLOYMENT.md:578-599`'s from-scratch recipe runs `config check` annotated "MUST be clean" at
exactly that point, so the shipped runbook fails as written. The `boot_posture.go:53` /
`doctor.go` "cannot drift" comment is contradicted by three of the doctor's four entry points.

## Fix
`validateConfigAtPath` and `runConfigCheck` use `config.LoadWithVault` and chain
`.WithVaultPresent(owner != nil)` — the doctor computes vault presence the same way the loader did
three lines earlier. Fix the "vault-blind callers stay strict" rationale comments.

## Done when
`config check` on a genesis-vault, zero-token config exits 0 (matching the daemon's admission
verdict) and the DEPLOYMENT.md from-scratch recipe runs as written.
