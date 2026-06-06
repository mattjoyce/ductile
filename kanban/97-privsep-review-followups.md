---
id: 97
status: todo
priority: Normal
blocked_by: []
tags: [privsep, review-followup, refactor]
---

# Privsep · luminary-review follow-ups (deferred from the Tier A+B fold)

> **Nav:** [[83-privsep-epic]] · source: `~/Obsidian/Personal1/ductile/privsep/PrivSep-Branch-Review_SYNTHESIS.md`

Non-blocking items from the 4-panel code review (all panels **approved, zero blockers**).
The safe/high-value Tier A+B items were folded 2026-06-07; these were deliberately deferred —
each for a stated reason, not an oversight.

## Deferred items

- **T3 — `ResolvedAccount` encodes one fact in two fields (`Confined` + `Source`).** (Hickey A1, O×L F3.)
  **Why deferred:** the *cheap* fix O×L literally suggested — `func (r ResolvedAccount) Confined() bool
  { return r.Source != AccountUnconfined }` — makes the **zero value confined** (`Source == "" !=
  "unconfined"`), a fail-OPEN footgun on the enforce seam. The *safe* fix is the sum-type refactor
  (uid/gid only exist in the confined arm) that **Hickey himself said "defer unless it earns its
  churn."** Do it as a deliberate refactor with the zero-value invariant tested, or leave it.

- **T5 — attestation verified twice per spawn for vault-principal plugins.** (Hickey B1+B2, O×L §4.1.)
  Sub-parts: (a) compute the attestation once per spawn and share it between `composePluginSecrets`
  and `bindAccountToFingerprint`; (b) distinguish *couldn't-check* (transient I/O) from *mismatch*
  (swap) in the verifier error so a flaky disk isn't escalated as "possible plugin swap"; (c) [doc,
  Tier D] document in the ADR that the principal downgrade path is unreachable (secret gate preempts).
  **Why deferred:** (a) is a risky refactor threading verification state through the spawn path in
  security code — mis-propagation = a *skipped* verification (regression) — for a reviewer-rated
  **minor** gain. Do it test-first (both principal and non-principal paths) or not at all.

- **T9 — `execute()`/`spawnPlugin` return a 7-value tuple** (O×L F4, T×F 20). Pre-existing shallow
  interface; privsep added 3 drop-failed return sites. Collapse to a `spawnResult` struct (would let
  privsep return a typed `DropFailed bool` instead of `errors.Is` downstream). Out of branch scope.

- **T15 — the `CapEff` hex parser (`process_unix.go`) is the one trust-bearing bespoke parser** (T×F 18).
  No fix; add a focused fuzz/edge test for the capability-mask parse (it fails closed today).

- **Vocab-drift guard** (T×F 19) — a lint/test that fails if `worker`/`account`/`run_as` vocabulary
  diverges across code comments, the ADR, and the schema. Prevents the #96 residue from recurring.

## Narrative
- 2026-06-07: Split from the Tier A+B fold. The fold took the safe, high-signal core (boot-time grant
  validation, named secrets surface, sentinel split, vocab cleanup); these five were deferred with
  explicit risk/benefit reasons (T3 footgun, T5 risky-for-minor, T9 pre-existing, T15 no-fix, lint).
  Not blocking the PR. (by @assistant)
