---
id: 145
status: idea
priority: Normal
tags: [shearline, datalog, auth, explain, config-check]
---

# Ductile as a consumer of the Datalog engine (and of Shearline)

Ductile's brief to the agent cutting `shearline/datalog` into its own module (board:
`datalog-module`), read against `shearline/docs/ENGINE.md`. Ductile is the third consumer.

## Through Shearline: token scopes

`shearline/examples/ductile` already models `internal/auth` (scopes as roles, the implication
table as `Inherits`, routes as grants) with a parity test: 154 tokens, 19 rows, no disagreement.
Adopting it would replace `normalizeScopes`/`HasAnyScope` and turn a bare 403 "insufficient
scope" into a refusal that names the missing fact. The engine needs here are Shearline's own:
cheap per request, bounds per request, proof and why-not.

## Direct: config-time questions (no per-request cost)

Evaluated once per `config check`, reload and `explain` (card #99), over facts built from the
loaded config, the vault principal table and `.checksums`:

1. **Who receives which secret, and why not.** `receives(Plugin, Secret)` from grants,
   principals, attestation and clearpass windows. Why-not answers today's most common
   operator question ("why did my plugin get nothing?") with the missing fact: unregistered,
   ungranted, unattested or fingerprint mismatch. That is card #99, done as data.
2. **Event reachability.** `reaches(Event, Pipeline)` over `on:`, `uses` steps and manifest
   `values.emit`, recursively. Flags cycles (a pipeline that retriggers itself), pipelines no
   emitter can reach, and emits nobody consumes.
3. **Baggage claims.** Every `{context.X}` in `with:` or `if:` is claimed upstream on every
   route that reaches the step. For-all as negation:
   `unclaimed(S, K) :- reads(S, K), reaches_step(E, S), not claimed_before(E, S, K).`
   Today a missing claim fails the job at runtime; this moves it to `config check`.

## What Ductile asks of the engine

- **Proof and why-not as data, not only rendered text.** Exported structs with stable fields,
  so `--json` can carry them; Ductile's operators are mostly LLM agents.
- **A compiled `Plan` and a `Base` safe for concurrent `Evaluate`.** Ductile compiles at config
  load and evaluates from a bounded worker pool; a reload swaps the plan.
- **Context first and bounded** (`Options`), never a panic on consumer input: Ductile's merge
  gates are gosec + golangci-lint + `-race`.
- **Stdlib only, Go 1.25,** and exported types that change rarely: Ductile would import both
  the engine and Shearline, the same diamond Anam has.
- **Deterministic answer bytes,** so `config check --json` output can be golden-tested.
- No aggregates, regex or arithmetic needed. `prefix/2` is enough; routing is exact-match.

## Not now

Nothing here blocks the cut or its first tag. Ductile adopts after Shearline v0.2.0.
