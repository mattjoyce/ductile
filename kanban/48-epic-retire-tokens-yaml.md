---
id: 48
status: backlog
priority: Normal
blocked_by: [9, 14]
tags: [vault, epic, back-compat, decomplect, tokens-yaml, deferred]
---

# Retire `tokens.yaml` — kill the graft, resolve against the live vault (EPIC)

**Origin (2026-06-05 design session):** while talking through [[43-vault-single-load-thread-nonce-boot]]
we realised most of the vault's residual complexity is the **`tokens.yaml` back-compat tax**, not the
vault itself. The clean test for back-compat: *does it have a reason to exist after the bridge is gone?*
The graft, `cfg.Tokens`, the freshness asymmetry, the fail-open question, the misnamed table — and even
`ductile vault import` — all fail that test. They are scaffolding to be demolished, not standing surface.

**End-state:** every `secret_ref` resolves against the single live vault owner (the one in-memory object,
`runtime.go:589`, already serving plugins via `Compose`). No copied table, no second decrypt, no
load-time freeze.

## What dies when the bridge goes (the back-compat column)
- The graft (`graftVaultSecrets` / `mergeVaultSecrets`) and the `cfg.Tokens` table.
- The graft's redundant decrypt (the back-compat half of [[43-vault-single-load-thread-nonce-boot]]).
- The webhook/relay **freshness asymmetry** (roll-needs-reload) — see [[45-docs-jun4-branch-review-corrections]] / Ousterhout §2.
- The [[41-vault-graft-fail-open-unresolvable-principal]] fail-open question (no graft → no question).
- `ductile vault import` itself — a one-way ratchet, removed *with* the bridge.
- The deferred `cfg.Tokens → resolvedSecrets` rename (#9 final note).

## Slices (tracer-bullet)
1. **Verify/cutover migration tool** (build on #9's `import`). Harden `import` into a one-time tool that
   not only imports but **proves parity**: for every `tokens.yaml` entry, assert the vault yields the same
   resolved value (now possible via [[42-vault-get-reserved-refusal-and-audit]]'s read path). Must:
   force an explicit per-entry decision on `${ENV}` indirections (don't silently freeze); be idempotent
   (never re-clobber a since-rolled value); offer a safe dry-run/verify mode runnable repeatedly *before*
   any destructive step; resolve same-name collisions definitively. **Built to be thrown away.**
2. **Flip the resolvers.** Point the load-time `secret_ref` consumers (webhook/relay) at the live vault
   owner instead of `cfg.Tokens`. Parity proven by slice 1's tool. Removes the freeze/freshness asymmetry.
3. **Demolish (destructive).** In one commit: delete the graft, `cfg.Tokens`, `tokens.yaml` support, the
   `import` command, and rename `→ resolvedSecrets`. Everything in the back-compat column dies together.

**Note:** `import` and the slice-1 tool are the *same lifecycle object* — born, used once, removed in
slice 3. Do not invest in `import` as permanent surface (no deep doc/skill coverage); carry a sunset
marker so it's never mistaken for load-bearing.

**Acceptance:** a fresh deploy with no `tokens.yaml` resolves every `secret_ref` from the vault; the
migration tool reports green parity on a real `tokens.yaml`; `grep -r tokens.yaml` finds only history.
