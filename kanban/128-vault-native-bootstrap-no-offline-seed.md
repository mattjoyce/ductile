---
id: 128
status: todo
priority: High
tags: [vault, bootstrap, regression, docs-drift, api, blocker]
---

# A from-scratch vault-native deploy with the API enabled cannot bootstrap (no offline seed path)

> **Nav:** root cause found while investigating [[118-system-tier-curate-trust-property-fixtures]] on
> the Dell, 2026-06-08. This is the *upstream* blocker that killed the docker fixture tier — a
> code/docs gap, not fixture staleness. Blocks [[118]] and [[116-testing-gate-green-only-governance]].

## The cycle (why nothing boots vault-native with API on)
1. `#94` made API bearer tokens **vault-only** — `api.auth.tokens[].secret_ref` resolves at boot,
   **fail-closed** (`internal/config/api_tokens.go` `ResolveAPITokens`; boot seam pinned by #119).
2. API enabled requires ≥1 token (`internal/config/loader.go:770`), and a literal `token:` is rejected
   (`loader.go:778`) — so the only legal form is `secret_ref`.
3. `secret_ref` resolves against the vault projection (`cfg.ResolvedSecrets`) built at load — the
   secret must already be **in the vault** when `system start` runs.
4. But `ductile vault set` **requires `--api-url`** (`cmd/ductile/vault.go:466`, sole-writer arch) — you
   can only write secrets **through the running daemon**.
5. The daemon can't start (step 3 fails closed) until the secret exists, and the secret can't be
   written (step 4) until the daemon starts. **Deadlock.**

## The documented escape doesn't exist
`docs/DEPLOYMENT.md §11` (line ~580) prescribes the offline seed:
```
ductile vault import --config "$CFG" --tokens "$CFG/tokens.yaml"   # local, key-touching, daemon stopped
```
and a `ductile config reconcile` step. **Neither command is implemented:**
```
$ ductile vault import   → Unknown vault action: import
$ ductile config reconcile → Unknown config action: reconcile
```
The `vault` action switch (`cmd/ductile/vault.go:36-59`) has: init, get, set, register-principal, roll,
revoke, revoke-principal, purge-principal, roll-principal, rotate-key, rotate-admin-token, help — **no
`import`.** `secrets` has keygen/encrypt/rotate. `config` has no `reconcile`. The skills doc
(`cmd/ductile/templates/skills-cli-commands.md:47`) also advertises `vault.import` — pure docs drift.
Meanwhile `tokens.yaml` is declared retired (epic #48: `internal/config/vault_secrets.go:16`
"there is no tokens.yaml and no merge"), so even the legacy fallback is gone.

## Evidence
- Ran on the Dell (cross-compiled linux/amd64, runner needs no Docker): `vault-secret-delivery` fails at
  `plugin lock` (literal api token), `webhook-ingress` fails at boot (webhook `secret_ref` not in vault).
- `config.test.yaml` (the supposed deploy template) has `secret_ref: ductile-api-admin` but **no
  `secrets:` block at all** — it cannot boot vault-native either. The whole tier is downstream of this gap.

## Do (decide the intended bootstrap, then make code + docs agree)
Pick the model and implement/restore it; this is a design call, not a mechanical fix:
- **Option A — offline seed (matches the docs):** implement `ductile vault import --vault --key --tokens`
  (and/or a generic offline `vault set` when the daemon is down, key-touching) so genesis can seed the
  API token before first boot. Smallest change to make the documented runbook real.
- **Option B — staged boot:** allow the daemon to start with the management `/vault/*` surface
  (vault-admin-token auth) available even when `api.auth.tokens` is unresolved/empty, so an operator can
  `vault set` the bearer token then the bearer-API activates (hot, or on reload). Changes the boot
  ordering / fail-closed contract — needs care against #94's intent.
- **Option C — genesis seeds it:** `vault init` (or a `--seed`/manifest) mints/accepts the initial API
  token as a normal (non-reserved) secret granted to `core` at genesis.

Then: reconcile `docs/DEPLOYMENT.md §11` + the skills CLI doc to whatever actually exists, and add a
fast in-process test that a from-scratch genesis→seed→boot path actually boots with API enabled.

## Done when
A documented, tested, from-scratch sequence brings up a vault-native gateway with the API enabled (no
literal tokens, no manual DB surgery), and `docs/DEPLOYMENT.md §11` matches the implemented commands.
Then [[118]] can migrate fixtures against a real template and [[116]] can gate the green set.
