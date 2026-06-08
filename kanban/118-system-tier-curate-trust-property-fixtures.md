---
id: 118
status: todo
priority: High
tags: [testing, docker, fixtures, vault, migration, trust]
---

# Curate the system tier to trust-property fixtures (18 → ~5)

> **Nav:** luminary council 2026-06-08 — **Thompson×Feathers** (selection criterion) + **Brooks×Beck**
> (migration sequence). Sibling: [[116-testing-gate-green-only-governance]],
> [[117-queue-state-machine-invariant-suite]].

## Problem
The ~18 docker fixtures accreted one-per-routing-feature, overlapping and undocumented, and 17/18
carry a literal `api.yaml` token #94 now hard-rejects at boot (the tier is 100% dead). Most of what
they "test" (routing, state) belongs in the in-process invariant suite [[117]]. **Keep the run.sh
black-box SEAM** (spawn real `ductile system start` → poll `/healthz` → drive via `ductile api` →
diff the artifact tree) — that's the agent's-eye surface, the right place. Cut the corpus.

## Do
Reduce to the small set of properties that can ONLY be proven against a live booted gateway:
- `boot-refuses-bad-config` — fail-closed at boot (see [[119-boot-refuses-unsafe-config]]).
- `secret-never-leaks-to-artifacts` — grep the whole artifact tree (logs/state/responses) for secret
  material, assert zero hits (Thompson's "test what leaks").
- `plugin-crash-leaves-deterministic-state` — kill a plugin mid-job, assert terminal state + state-dir.
- real `webhook-ingress` (socket + token auth).
- one polyglot subprocess round-trip (the `sys_exec`/python path).

Migration (Brooks×Beck, smallest-steps):
1. **Tracer:** port `webhook-ingress` (smallest, high-value ingress+auth) to vault-native using the
   `vault-secret-delivery/run.sh` template (genesis + grant in run.sh; drop the literal token). Ship.
2. Extract the genesis boilerplate into `scripts/test-docker-lib` (`fixture_vault_init`,
   `fixture_grant_token`) — only AFTER one caller exists.
3. For every remaining fixture write ONE sentence: "proves X, which nothing else proves." Can't?
   **Delete it** — don't migrate. The from-scratch energy goes into saying NO to fixtures.

## Anti-goals (over-engineering traps to avoid)
No fixture DSL / YAML-driven harness (bash + real CLI is the right altitude). No `ductile test`
subcommand (orchestration stays in `scripts/`, per TESTING.md §1). No one-fixture-per-feature.

## Done when
The tier is ~4-5 documented fixtures, each booting vault-native and naming a unique live-only risk;
all remaining fixtures deleted; the green set fed to [[116-testing-gate-green-only-governance]].

## GROUND-TRUTH FINDING — 2026-06-08 (Dell, cross-compiled linux/amd64 binary, ran run.sh directly)
**The handoff's premise is wrong: `vault-secret-delivery` is NOT a working vault-native template —
it is also broken.** Ran the fixtures on the Dell (the `test-docker` runner needs no Docker; it just
execs each `run.sh`, which spawns the real binary + curls localhost):
- **`vault-secret-delivery`** → FAILS at `plugin lock` (before boot):
  `api.auth.tokens[0]: a literal token value is not allowed — use secret_ref (#94)`. Its
  `config/api.yaml` still carries the literal `token: "test-admin-token"`. So even the "template"
  never migrated. **`scripts/test-docker-lib` does NOT yet have `fixture_vault_init`/`fixture_grant_token`.**
- **`webhook-ingress`** → FAILS at boot:
  `webhook[0] (/webhook/github): secret_ref "github_webhook_secret" not found in the vault`. It has
  no vault configured at all (no `secrets:` block, no genesis in `run.sh`), and its `tokens.yaml`
  literal-secret store is the retired pre-#94 mechanism — webhook secrets now resolve from the vault
  projection too (`internal/webhook/config.go` is fed `cfg.ResolvedSecrets`).
- **The REAL vault-native pattern lives in `config.yaml` / `config.test.yaml`:**
  `api.auth.tokens: [{secret_ref: ductile-api-admin}]`, resolved against the vault projection at load.

### Corrected migration recipe (per fixture)
1. `config/api.yaml`: drop literal `token:`, use `secret_ref: <name>` (e.g. `ductile-api-admin`).
2. Move every webhook/token secret out of `tokens.yaml` into the vault; keep the `secret_ref:` in
   `webhooks.yaml`. Delete `tokens.yaml`.
3. `config/config.yaml`: add the `secrets: {age_key_file, vault_file}` block.
4. `run.sh`: genesis (`secrets keygen` + `vault init`), then `vault set` each gateway secret granted
   to the `core` principal so the load-time projection (`cfg.ResolvedSecrets`) includes it, THEN
   `config lock` + `plugin lock` + `system start`. (Open question to verify: does `vault set
   --principal core` surface the secret in `ResolvedSecrets` at load? That projection mechanic is the
   one thing the tracer must prove on the Dell.)

### Reordering this implies
The intended sequence ("port webhook-ingress *using the vault-secret-delivery template*") is broken
because the template doesn't work. Corrected order: **first make ONE fixture genuinely vault-native
(fix `vault-secret-delivery`, the smaller surface — api.yaml only), prove the projection mechanic,
extract `fixture_vault_init`/`fixture_grant_token` into `scripts/test-docker-lib`, THEN port
`webhook-ingress` (api token + webhook secret + tokens.yaml retirement).** This re-scopes step 1.

### THE BLOCKER to resolve first (bootstrap chicken-and-egg)
`ductile vault set` **requires `--api-url`** — vault writes go through the running daemon (sole-writer
arch, `cmd/ductile/vault.go:466`). But an **API bearer token must already be in the vault at BOOT**
(resolved at load by `ResolveAPITokens` before the listener opens). So: you can't `vault set` the api
token before boot, and the daemon won't boot without it. The deployment template (`config.test.yaml`,
`secret_ref: ductile-api-admin`) has the same shape — **how does it seed `ductile-api-admin` into the
vault before first boot?** That seam is unknown and must be found/built before any fixture can boot
vault-native with API enabled. Candidates to investigate: an offline seed/import path (none obvious in
`vault` subcommands — only `init`/`rotate-key`/`rotate-admin-token` are key-touching offline ops); or
boot-with-API-disabled → `vault set` → restart; or `vault init` seeding initial secrets.
Also: `tokens.yaml` is documented as a *legacy fallback* for `secret_ref` (`docs/WEBHOOKS.md`), but
`webhook-ingress` still failed `secret_ref "github_webhook_secret" not found in the vault` **with
`tokens.yaml` present** — so the legacy-fallback resolution path may itself be broken/unwired and is
worth a separate check.

**Evidence:** cross-compiled `linux/amd64` (`CGO_ENABLED=0`, modernc sqlite = pure Go) binary ran on
the Dell against `scripts/test-docker-runner {vault-secret-delivery,webhook-ingress}`; the runner needs
no Docker (it just execs `run.sh`). Both failures captured above.
