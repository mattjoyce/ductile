---
id: 75
status: done
priority: Low
blocked_by: []
tags: [config, decomplect, hickey, tech-debt, drift]
---

# Collapse the duplicated per-plugin default resolvers onto one source

> Closed (2026-06-06) — **structural half built; one resolver, all sites read from it.**
> Introduced `config.ResolvedPluginConf` (built via `config.ResolvePluginConf(raw, maxWorkers)`):
> the single place the `value-if-set(>0)-else-DefaultPluginConf` rule lives, exposing
> `MaxAttempts/BackoffBase/Timeout(cmd)/BreakerThreshold/BreakerResetAfter/Parallelism`.
> Every former duplicate now delegates to it — `MaxAttemptsForPlugin`, dispatcher
> `getTimeout` + `computeRetryDelay`, scheduler `breakerThreshold`/`breakerResetAfter`, and the
> `EffectivePluginConf` view (values from the resolver; the view keeps only provenance). The two
> non-pure twists survive: dispatcher `pluginParallelism` still applies the manifest
> `concurrency_safe:false` clamp on top of the resolved base, and `Timeout(cmd)` keeps the
> per-command `Overrides` precedence. Acceptance test `TestEffectivePluginConfMatchesAllRuntimePaths`
> rebuilds the resolver with each runtime site's exact call shape and pins the view to it
> field-for-field across unset/explicit/partial configs — a divergent inline resolver now fails the
> suite. `go build ./... && go test ./...` green. Behaviour-identical (literals already equalled defaults).
>
> ---
> Earlier progress (2026-06-06) — **drift trap closed + guarded.** The one site that genuinely
> *re-hardcoded* default values, dispatcher `getTimeout` (`60/120/10/30s` literals), now resolves
> from `config.DefaultPluginConf().Timeouts` like everyone else (behaviour-identical — the literals
> already equalled the defaults). The other five sites (`MaxAttemptsForPlugin`, `computeRetryDelay`,
> `breakerThreshold`/`breakerResetAfter`, `EffectivePluginConf`) already single-source from
> `DefaultPluginConf`; only `getTimeout` carried a copy. Added drift guards:
> `TestEffectivePluginConfUnsetEqualsDefaults` (pins the view's unset resolution to `DefaultPluginConf`
> for every field) and `TestGetTimeoutMatchesEffectiveView` (the runtime timeout path must equal the
> `--effective` view per command). `go build ./...` + `go test ./...` green (the lone failure,
> `TestSpawnPluginTimeoutKillsProcessGroup`, is the known pre-existing flake — passes isolated).
>
> **Remaining (the structural half, now low-urgency since drift is guarded):** the card's "one
> resolver object that dispatcher/scheduler/view all read from" is not built — the resolution *logic*
> (value-if->0-else-default) is still expressed per-site, just no longer with duplicated default
> *values*. A `ResolvedPluginConf` type the hot paths consume would finish the decomplect; deferred
> because the hot-path signatures (sub-config pointers) make it a larger, behaviour-sensitive change
> and the silent-drift risk — the actual danger — is now covered by tests.

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
