---
id: 78
status: done
priority: Medium
blocked_by: []
tags: [router, dsl, conditions, hickey, decomplect, predicates, sprint-17]
---

# Make "this predicate needs context" an explicit, validated contract

**DONE 2026-06-06.** `compilePipeline` (`internal/router/dsl/compiler.go`) now rejects an `on-hook:`
pipeline whose `if:` references `context.*` — context is provably never available at hook time, so
such a predicate could only ever silently dead-route. Error at load:
`on-hook trigger predicate cannot reference context.* — context is not available at hook time`.
`on:` triggers are unchanged (context.* there is path-dependent, covered by the runtime warn in card
77). Test `TestCompileSpecsRejectsHookContextPredicate` covers reject (hook+context.*) and accept
(hook+payload.*). `docs/PIPELINES.md` "Context availability at trigger time" updated to document the
rejection. This makes the param in card 80 provably dead. Router + dsl suites green.

**Origin: Hickey×Armstrong audit of #75, 2026-06-06. Finding H1 (Hickey: value complected with dispatch path) + H4 (easy over simple).**

`conditions.Scope` (`conditions/types.go:35-39`) exposes `payload`, `context`, `config`, and
`validatePath` (`conditions/validate.go:103-119`) accepts `context.*` in **any** trigger `if:`.
But whether `context` is populated depends entirely on the runtime path that reached the route:

| Entry path | `Scope.Context` source | Populated? |
|---|---|---|
| intra-chain `Next` | `dispatcher.go:862-904` (decodes `AccumulatedJSON`) | yes |
| hook `NextHook` | `dispatcher.go:1915` (`nil`) | never |
| relay-root `Next` | `relay/root.go:28` (unset) | never |

The same predicate string means a real test on one path and a constant on two others, and nothing in
the DSL tells the author which world they are in. The value (the predicate) is pure, but its
*resolvability* is complected with the call site — Sprint 17 took the easy path (reuse the existing
`Scope.Context` field) instead of the simple one (make the dependency explicit and checkable).

**Do:** make the context dependency a first-class, validated fact. e.g. at compile time, detect when
an entry route's `If` references `context.*` and the route is a class that never carries context
(hook / relay-root) → reject at load with a clear message ("hook trigger predicate cannot reference
context.* — context is not available at hook time"). This turns H1 + the runtime half (card 77) into
a load-time guarantee for the paths that can never satisfy it.

**Why:** authors should learn at config-lock, not via a silently-dead pipeline, that a predicate is
unsatisfiable on its route class. Decomplects predicate meaning from dispatch path.

**Acceptance:** a hook/relay-root entry predicate referencing `context.*` fails at load with a
path-class-specific error; an intra-chain predicate referencing `context.*` still loads; doc note in
PIPELINES.md states which roots are available per trigger class. Relates to 77 (runtime) and 80
(the unwired NextHook context path).
