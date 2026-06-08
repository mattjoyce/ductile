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
