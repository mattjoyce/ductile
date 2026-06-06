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

## Findings → cards
- **76** (A4, Med) — `from_plugin:` not validated against the registry; typo = permanent dead route.
- **77** (A1, High) — nil/absent `context` makes a predicate silently constant-false (no supervisor).
- **78** (H1+H4, Med) — predicate resolvability is complected with dispatch path; make the context
  requirement an explicit, load-validated contract.
- **79** (A2, High) — one poison predicate aborts all co-triggered routes + later events; no per-route
  fault isolation.
- **80** (H2, Low) — `NextHook` `sourceContext` param is built/threaded/tested but has no producer
  (passes `nil`); wire it or remove the speculative surface.

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
