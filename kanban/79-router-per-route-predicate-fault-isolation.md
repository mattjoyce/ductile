---
id: 79
status: backlog
priority: High
blocked_by: []
tags: [router, conditions, armstrong, fault-isolation, blast-radius, sprint-17]
---

# Isolate predicate-eval failures per route instead of aborting the batch

**Origin: Hickey×Armstrong audit of #75, 2026-06-06. Finding A2 (Armstrong: routes must fail independently).**

In `Next`, a predicate eval error returns immediately and discards every route accumulated so far:

```
ok, err := conditions.Eval(route.Source.If, ...)
if err != nil { return nil, fmt.Errorf("pipeline %q: evaluate trigger if: %w", ...) }  // engine.go:157-160
```

The dispatcher propagates it (`dispatcher.go:908-911`), which aborts the remaining events in the
`for i := range events` loop (`dispatcher.go:882`). So a **single** malformed predicate — e.g.
`context.count gt 1` when an upstream put a string there (a runtime type mismatch, since
`validate.go` only checks the literal value's type, not the resolved path's — finding A3) — takes
down routing for **all** well-formed pipelines that matched the same event, plus every later event
from that emitting job. One poison predicate is a shared-fate fault for every co-triggered route.

(`NextHook` is already better: its error only logs and returns — `dispatcher.go:1916-1918` — but it
is still all-or-nothing across routes for that signal.)

**Do:** treat a per-route predicate eval error as a skip-with-`Warn` for that route, not a hard abort
of the batch. Continue evaluating the other matched routes; surface the failing route id + pipeline.
Decide intentionally whether a predicate error should ever be fatal (probably not — a route that
can't decide should fail safe by not matching, loudly).

**Why:** Armstrong's core question — can each part fail independently? Today it cannot; one author's
mistake silently (well, fatally) suppresses unrelated pipelines on the same event.

**Acceptance:** an eval error on one matched route logs a `Warn` and does not prevent other matched
routes (same event) from resolving; remaining events in the dispatch loop still process; test covers
a poison predicate co-resolved with a healthy route.
