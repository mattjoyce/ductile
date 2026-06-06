---
id: 80
status: backlog
priority: Low
blocked_by: []
tags: [router, hickey, yagni, speculative-generality, tech-debt, sprint-17]
---

# `NextHook` sourceContext is built and threaded but has no producer — wire it or remove it

**Origin: Hickey×Armstrong audit of #75, 2026-06-06. Finding H2 (Hickey: speculative generality).**

`NextHook(ctx, plugin, signal, payload, sourceContext map[string]any)` (`internal/router/engine.go:197`,
`internal/router/interface.go:62-69`) carries a `sourceContext` parameter, a paragraph of interface
doc, and full predicate-evaluation wiring (`engine.go:228-240`). `engine_test.go` (+358 lines)
exercises it, so coverage *looks* complete. But the **sole production caller passes `nil`** and says
so:

```
// ... by the time we're here the upstream job has no accumulated durable context.
// Hook entry-route predicates therefore see Scope.Context as nil today.
// The plumbing is in place for future architectures that expose context at hook time.
dispatches, err := d.router.NextHook(ctx, job.Plugin, signal, payload, nil)   // dispatcher.go:1910-1915
```

This is interface surface + contract doc added for a capability with zero live producers. Combined
with finding A1/card 77, it is actively harmful: it advertises a feature (gate hook fan-out on
upstream baggage) that silently does nothing.

**Do:** make a call. Either (a) **wire a producer** — if a real architecture needs context at hook
time, populate `sourceContext` from the source job's accumulated context (mirroring
`dispatcher.go:862-879`) and lift the short-circuit; or (b) **remove the speculative surface** — drop
the `sourceContext` param from `NextHook`, the interface doc promising it, and the tests that only
prove unreachable behavior. Keep the seam minimal until there is a caller.

**Why:** YAGNI / "don't carry a contract you can't honor." An unwired-but-documented param is worse
than no param: it reads as supported.

**Acceptance:** either `NextHook` receives a real (non-nil, populated) context from a production
caller with a test exercising the live path, OR the parameter and its doc/tests are removed and
hook predicates referencing `context.*` are rejected at load (see card 78). No dead "future
architecture" surface left behind.
