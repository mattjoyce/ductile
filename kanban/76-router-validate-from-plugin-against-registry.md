---
id: 76
status: done
priority: Medium
blocked_by: []
tags: [router, dsl, validation, from_plugin, armstrong, silent-misroute, sprint-17]
---

# Validate `from_plugin:` against the plugin registry at load

**DONE 2026-06-06.** `validateFromPluginExists` added (`internal/router/engine.go`), called from
`LoadFromConfigFiles` on the `registry != nil` path right after `validateUsesNodesExist`. A pipeline
whose `from_plugin:` names an unregistered plugin now fails at load with
`pipeline %q: from_plugin references unknown plugin %q`; empty `from_plugin:` and `registry == nil`
(compile-only) paths are unchanged. Test `TestValidateFromPluginExists` covers known / empty /
unknown. `go build ./...`, `go vet ./internal/router/...`, and `internal/{router,api,doctor}` tests
green.

**Origin: Hickey×Armstrong audit of #75 (Sprint 17 context-aware route predicates), 2026-06-06. Finding A4.**

`validateUsesNodesExist` (`internal/router/engine.go:526-538`) checks every `uses:` step against
the plugin registry at load, but **nothing validates `from_plugin:`**. A typo —
`from_plugin: whispr` instead of `whisper` — compiles clean, passes config-lock, and then
`sourcePluginMatches` (`engine.go:262-268`) returns `false` for every event forever. The pipeline
silently never fires, with no error at load and only a `Debug` line at runtime
(`engine.go:149-154`, `engine.go:221-226`).

This is the same silent-misroute class as the nil-context predicate (card 77) and the unsatisfiable
`context.*` path (card 78): config that validates but quietly no-ops. The registry is already in
hand at the exact spot the check belongs.

**Do:** in `validateUsesNodesExist` (or a sibling validator on the same `registry != nil` path in
`LoadFromConfigFiles`, `engine.go:30-40`), reject any pipeline whose `FromPlugin` names a plugin not
in the registry. Mirror the existing error shape: `pipeline %q: from_plugin %q references unknown
plugin`.

**Why:** load-time failure is free here and converts a permanent silent dead-route into an immediate
config error. Highest leverage / smallest diff of the audit.

**Acceptance:** loading a pipeline with `from_plugin:` naming an unregistered plugin fails at load
with a clear error; a valid `from_plugin:` still loads; test covers both. No behavior change when
`registry == nil` (compile-only paths).
