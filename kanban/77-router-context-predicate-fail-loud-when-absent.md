---
id: 77
status: backlog
priority: High
blocked_by: []
tags: [router, conditions, predicates, armstrong, silent-misroute, fault, sprint-17]
---

# A `context.*` predicate must fail loud when context is structurally absent

**Origin: Hickey×Armstrong audit of #75, 2026-06-06. Finding A1 (Armstrong: silent fault, no supervisor).**

When `Scope.Context` is nil, `ResolvePath` (`internal/router/conditions/paths.go:40-47`)
type-asserts the nil map (`ok=true`, `obj=nil`), reads a missing key, and returns
`(present=false, nil, nil)` — **no error**. So a trigger predicate `if: {path: context.role, op: eq,
value: admin}` evaluates `false` and the pipeline silently never fires.

Context is nil on two of the three entry paths:
- hook routing — `dispatcher.go:1915` passes `nil` (sole production `NextHook` caller),
- relay-root routing — `relay/root.go:28` sets no `SourceContext`.

So **every** `context.*` hook/relay-root entry predicate is a permanent constant-false today. The
only trace is a `Debug` log (`engine.go:234`, `engine.go:162`). "Context not provided here" and
"context provided but key missing" collapse to the same answer with no operator-visible signal.

**Do:** distinguish *root unavailable* from *key missing*. Options (pick per design): (a) in
`ResolvePath`, when a non-`payload` root is referenced but its map is nil, return an error/typed
sentinel that the router surfaces as a `Warn` (not a silent false); or (b) gate at the call site —
if a route's `If` references `context.*` and the request carries no context, log `Warn` with
route id + pipeline. Pair with card 78 (load-time contract) for the author-facing half.

**Why:** a routing decision that depends on data that structurally cannot be present should be
observable, not a quiet drop. This is the deepest single fix of the audit — it (with 76, 78) closes
the whole "validates but silently no-ops" family.

**Acceptance:** a `context.*` predicate evaluated with nil context produces a `Warn`-level signal
(or load-time rejection via 78), distinguishable from a present-but-missing key; payload-only
predicates are unchanged; test covers nil-context vs present-empty-context.
