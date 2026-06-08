---
id: 100
status: done
priority: High
blocked_by: []
tags: [privsep, correctness, v1.0, review-followup]
---

# Privsep · ResolvedAccount sum-type — close the `Confined()` zero-value footgun (T3)

> **Nav:** [[83-privsep-epic]] · split out of [[97-privsep-review-followups]] as the **one v1.0-relevant
> correctness item** from the luminary review (Ousterhout×Liskov / Hickey×Armstrong, T3).

**Job story:** *When* the resolve→enforce seam returns a `ResolvedAccount`, *I want* an "unconfined"
verdict to be an **explicit choice**, never a zero value, *so* an error path can't silently produce a
`ResolvedAccount{}` that reads as "legitimately runs at gateway uid" and waves a plugin through unwalled.

## The smell (from the review — Hickey A1, O×L F3)

`ResolvedAccount` encodes **one fact in two fields** (`Confined bool` + `Source`) — `internal/dispatch/account.go`.
Both naive consolidations are footguns on the enforce seam:
- The *cheap* fix O×L literally suggested — `func (r ResolvedAccount) Confined() bool { return r.Source
  != AccountUnconfined }` — makes the **zero value read as confined** (`Source == "" != "unconfined"`)
  with `uid/gid == 0` → a drop to **root**: **fail-OPEN**.
- The current bool field has the opposite zero-value hazard (`Confined:false` → spawns unconfined by
  accident, read as by-design).

Either way a zero/error `ResolvedAccount` must never reach spawn as a valid verdict. The *safe* fix is
the **sum-type** Hickey flagged as "defer unless it earns its churn": uid/gid exist only in the
confined arm, so an unconfined/error value structurally cannot carry a droppable identity.

## Acceptance
- A `ResolvedAccount` zero value cannot reach spawn as a valid unconfined drop — enforced by type
  shape (explicit `Source`/state constructor) or an invariant check at the resolve→enforce seam.
- Every construction path goes through a constructor that *names* the state; raw struct literals with
  an implicit `Confined:false` are eliminated (or guarded).
- Negative test: an error/zero-value `ResolvedAccount` injected at the seam fails the spawn closed,
  never spawns unconfined.
- No behaviour change for the existing valid paths (granted/default/downgraded/explicit-unconfined).

## Narrative
- 2026-06-07: Promoted from #97 to its own card during the v1.0 triage. It is the only deferred review
  item with a *correctness* (not polish) smell, so it sits on the v1.0 line; T5/T9/T15/vocab-lint stay
  in #97 as v1.x polish. (by @assistant)
- 2026-06-07: DONE — added `ResolvedAccount.Validate()` (seam guard) called by applyAccountCredential:
  a confined verdict must be uid>0/gid>0 (never root), unconfined carries no id → else fail closed
  (ErrAccountDropFailed). Chose the invariant-check over the full sum-type refactor (lower risk, same
  guarantee). Matrix + seam tests. (by @assistant)
