---
id: 94
status: todo
priority: High
blocked_by: []
tags: [vault, api, security, secret-ref, config, adr-8.5, v1.0]
---

# Move api.yaml bearer tokens into the vault

ADR §8.5: "if it's a secret, it's in the vault."  The API auth tokens in
`cfg.API.Auth.Tokens` (api.yaml `auth.tokens[].token`) are secrets — they are
the credentials a caller must hold to drive the Ductile REST API.  They
currently live as literal values or `${ENV_VAR}` interpolation in config,
outside the vault.  The vault epic (#48) closed with this class of secret
explicitly confirmed as "config-native, wholly independent" — an accurate
description of the *current* state, not an endorsement of it.  The Unraid
admin's observation surfaced the gap.

## What needs building

1. **Loader support for `secret_ref:` in `auth.tokens[].token`.**  The
   `projectVaultSecrets` projection currently handles plugin config and webhook
   secrets.  It needs to cover the API auth token values too — either by
   extending the projection pass to walk `cfg.API.Auth.Tokens`, or by
   introducing a `secret_ref:` scalar resolver applied at the same load point.
   After projection, `cfg.API.Auth.Tokens[].Token` holds the resolved value;
   the vault reference is never written to a snapshot or log.

2. **Vault entries for each token.**  One vault secret per logical token
   (e.g. `ductile-api-admin`, `ductile-api-readonly`).  Principal assignment
   TBD — either a new reserved `api` principal class or the existing `core`
   principal.  The genesis flow (or a dedicated `vault set`) provisions them.

3. **Retire `${DUCTILE_TOKEN_ADMIN}` env-var path.**  Once vault-sourced,
   the env var is dead.  Remove it from `config.yaml` examples, docs, and any
   deploy scripts.  Add a loader warning if a literal or `${ENV}` value is
   found in `auth.tokens[].token` after projection (migration aid).

4. **Startup ordering.**  The vault must be unlocked before `auth.Authenticate`
   can serve any request.  This is already true for the plugin/webhook secret
   path (fail-closed boot gate).  Confirm `cfg.API.Auth.Tokens` is populated
   from `cfg.ResolvedSecrets` *after* `projectVaultSecrets` runs and *before*
   the HTTP listener opens — audit `runtime.go` boot sequence.

5. **Doctor / selfcheck coverage.**  `doctor.go` currently validates that
   `cfg.API.Auth.Tokens` is non-empty.  Extend to verify each token resolved
   (non-empty string after projection) so a missing vault entry is caught at
   boot, not at first request.

## Acceptance

- `api.yaml` on all three hosts carries `secret_ref:` values for all bearer
  tokens; no literals or `${ENV_VAR}` remain in that file.
- `grep -r DUCTILE_TOKEN_ADMIN` finds only history.
- A fresh deploy with a correctly provisioned vault boots clean and
  authenticates successfully.
- A deploy with a missing vault entry for an api token fails at boot
  (doctor/selfcheck), not silently at request time.

## Narrative
- 2026-06-07: Confirmed a **v1.0 gate** (operator) — API bearer tokens are secrets and must live in
  the vault before v1.0 ("if it's a secret, it's in the vault", ADR §8.5). Priority → High, tagged
  v1.0. **Separate branch / scope — NOT the privsep branch** (this is vault/API loader work, not uid
  separation). Tracked on the v1.0 line in [[102-v1.0-readiness-privsep-ship-line]]. (by @assistant)
