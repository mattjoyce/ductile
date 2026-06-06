---
id: 75
status: backlog
priority: Low
blocked_by: []
tags: [config, decomplect, hickey, tech-debt, drift]
---

# Collapse the duplicated per-plugin default resolvers onto one source

**Origin: #71 (`config show --effective`) + its /simplify altitude review, 2026-06-06.**

The "value if set (>0) else `DefaultPluginConf()`" resolution for a plugin's retry/timeout/
breaker/parallelism fields is re-implemented in **six** places that must stay in lockstep:

- `config.MaxAttemptsForPlugin` (retry.max_attempts)
- dispatcher `getTimeout` (timeouts.*), `computeRetryDelay` (retry.backoff_base),
  `pluginParallelism` (parallelism)
- scheduler `breakerThreshold` / `breakerResetAfter` (circuit_breaker.*)
- `config.EffectivePluginConf` (#71 — the new view, the 6th site)

`DefaultPluginConf()` single-sources the default *values*, but the resolution *logic* is copied.
If a default changes or a new field is added, every site must be updated by hand; a miss makes the
`--effective` view (or a runtime path) silently disagree with the others — the exact hidden-state
class #71 set out to kill.

**Do:** make `EffectivePluginConf` (or a small `ResolvedPluginConf` type) the single resolver and
have the dispatcher/scheduler/MaxAttemptsForPlugin read their values from it, instead of each
re-deriving. Keep `DefaultPluginConf` as the value source. Watch the two non-pure twists:
`pluginParallelism`'s manifest `concurrency_safe:false` clamp, and `getTimeout`'s per-command
`Overrides` lookup — both must survive the unification.

**Why deferred:** #71 deliberately scoped to the *view* and did not touch dispatch/scheduler hot
paths. `TestEffectivePluginConfMatchesRuntimeResolver` currently guards only the max_attempts path
against drift; the rest rely on the values staying put. Low priority, but it removes a standing
drift trap.

**Acceptance:** one resolver computes a plugin's effective retry/timeout/breaker/parallelism
values; dispatcher, scheduler, and the `--effective` view all read from it; a test cross-checks
the view against each runtime path (no silent drift possible).
