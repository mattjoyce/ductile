---
id: 81
status: done
priority: Medium
blocked_by: []
tags: [review, hickey, armstrong, router, sprint-17, lens-pass]
---

# Review: Hickey × Armstrong pass on #75 (context-aware route predicates)

**Lens pass, 2026-06-06.** Structure-at-rest (Hickey) × behaviour-under-fault (Armstrong) audit of
GitHub PR #75 / branch `hickey-sprint-17-context-aware-route-predicates` (Sprint 17): `from_plugin:`
source scoping + context-aware `if:` trigger predicates. Feature merged into the current branch
(commits `84fea4e`, `c94d79c`, `4cc967d`, `f52e85d`, `e71b49a`, all ancestors of HEAD). "After —
code audit" variant.

## Throughline
#75 added a predicate language whose power (context, cross-plugin scoping) outran its **contract**.
Three findings are the same shape — *config that validates at load but silently no-ops at runtime* —
and the deepest single fix is to make an unsatisfiable predicate/selector a **loud failure** (load-
or eval-time) instead of a quiet `false`.

## Findings → cards — ALL DONE 2026-06-06
- **76** (A4, Med) ✅ — `from_plugin:` now validated against the registry at load; typo fails config-lock.
- **77** (A1, High) ✅ — `Warn` when a `context.*` predicate runs with structurally-nil context.
- **78** (H1+H4, Med) ✅ — `context.*` in an `on-hook:` predicate is rejected at config load.
- **79** (A2, High) ✅ — per-route predicate-eval faults skip-with-warn instead of aborting the batch.
- **80** (H2, Low) ✅ — dead `NextHook` `sourceContext` param removed (78 made it provably unreachable).

Whole-suite `go build ./...` + `go test ./...` green (29 packages) after the five fixes.

## Noted, not carded
- **H3** — `currentCtx.AccumulatedJSON` is decoded by 4 separate readers at `dispatcher.go:870-879`
  (#75 added the 4th); candidate for a single typed accumulated-context view. Pre-existing; fold into
  a future decomplect card if it recurs.
- **A3** — predicate type-correctness depends on producer data shape with no contract; mitigated by
  fixing 79's blast radius.

## Credit (clean under the lens)
Route eval is pure and bounded (`MaxDepth`/`MaxPredicates`, `conditions/validate.go:16-22`); no
external calls in the resolution path, so no timeout/supervisor gap there. Exposing `compiled_routes`
via `/config/view` is a real observability win.

## Cluster / order
76, 77, 78 are one family (the "validates but silently no-ops" trap) — 76 is the cheapest standalone
win; 77+78 are the runtime+load halves of the context contract. 79 is independent and high-value.
80 is a decision (wire vs delete) that 78 informs.
