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
- 2026-06-08: **Hickey × Armstrong design review** (branch `feat/94-api-tokens-to-vault`, branched off
  privsep tip `cfeef90` to match what ThinkPad runs). The shipped vault already embodies the absorbed
  H×A feedback (`vault_secrets.go:108` cites "Rev2 §1.2"); the points below hold *this* spec to that
  bar, they are not a regression.
  - **Adopt the existing `SecretRef` pattern; do NOT overload `Token`.** Webhook/relay carry a
    separate `SecretRef` field distinct from the resolved value (`webhook/config.go:26`,
    `relay/config.go:23`). Item 1's "introduce a `secret_ref:` scalar resolver" sniffs one stringly
    field to decide value-vs-pointer — Hickey complecting. Add `APIToken.SecretRef` as a sibling of
    `Token` (`types.go:157`). Decision before code, not a string prefix.
  - **Don't braid resolution into projection.** `projectVaultSecrets` builds `cfg.ResolvedSecrets`
    (available secrets); *binding a consumer* is a separate consume-time pass — that's how
    webhook/relay already work. Resolve API tokens next to `runtime.go:656`, reading `ResolvedSecrets`;
    leave the projection alone.
  - **Don't overwrite `Token` in place.** Item 1 ("Token holds the resolved value") destroys the
    provenance item 3's migration-warning needs — post-overwrite a resolved ref and an operator
    literal are byte-identical. Keep the ref in its own field; warn on input shape.
  - **Failure has no supervisor today.** Resolved-empty token → listener starts anyway
    (`runtime.go:700`); doctor only *warns* (`doctor.go:491`). Make it fail-closed by extending the
    existing `admission.RequireAPIAuth` gate (`runtime.go:483`) from "zero tokens" to "zero *resolved*
    tokens." Validate-time stays warn-when-blind (`validator.go:24`); the **keyed daemon boot** owns
    the hard stop. Doctor (item 5) is advisory and the wrong owner.
  - **Declare the reload contract.** `runtime.go` has a live reload/listener-swap path
    (`runtime.go:178-229`); a reload that can't resolve a token must abort the swap and keep the old
    listener serving auth (snapshot-at-reload, matching webhook/relay). The card is boot-only — say it.
  - **Principal choice (item 2) is a correctness decision, not "TBD" — and the card's framing is a
    false choice.** Kind enum is *closed*: `plugin`/`consumer`/`gateway` (`store.go:21-23`) — there is
    no `api` kind (a "new class" = extending the enum). `core` is a *reserved name* holding the
    fingerprint nonce (`genesis.go:14`, `fingerprint.go:9`), not a class — granting api authz onto it
    complects bootstrap identity with API authz. Resolve-by-kind: `pluginScopedSecret` excludes a
    secret only when every grantee is `KindPlugin` (`vault_secrets.go:152`), so a plugin-granted api
    token would *silently never resolve*. **Recommend a dedicated `consumer`-kind principal** (e.g.
    `ductile-api`): explicit, auditable, exercises the typo/blast-radius guard
    (`unregisteredGrantee`, `vault_secrets.go:136`), no enum change, `core` untouched. Acceptable v1.0
    shortcut: a secret with *no* grant (resolves via the `len==0` branch, `vault_secrets.go:153`) —
    but it's invisible to the typo guard. (by @assistant)
- 2026-06-08: **Loader/runtime mechanism implemented** (commit on `feat/94-api-tokens-to-vault`).
  Mechanism done per the review above: `APIToken.SecretRef` sibling field; `config.ResolveAPITokens`
  resolves against `cfg.ResolvedSecrets` and is fail-closed (unresolvable/empty/both/neither → hard
  error); `buildRuntime` resolves before the listener opens (named supervisor; reload restores the
  old runtime on error); `validateAPITokens` (warn-when-blind); doctor false-warn fixed; schema
  accepts `secret_ref`. A literal token still boots with a migration warning (backward compatible).
  New tests prove the empty-credential path fails closed. `go build/vet/test ./...` green (one
  unrelated flaky `dispatch` process-group test, passes in isolation).
  - **What's covered:** items 1 (loader), 3 (loader warning), 4 (startup ordering + reload), and the
    *advisory* part of 5 (doctor). The hard resolution check lives at boot, not doctor — per the review.
  - **Still open (deploy-coordinated, deliberately not in the code commit):** item 2 — seed the vault
    secret under a `consumer`-kind principal via genesis; then item 3's example/doc migration
    (`config.yaml`, `config.test.yaml`, `README.md` still use `${DUCTILE_TOKEN_ADMIN}`) and the
    three-host `api.yaml` cutover. Left for the host/test pass so the default config doesn't fail
    closed before the vault entry exists. (by @assistant)
