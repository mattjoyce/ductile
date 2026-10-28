---
id: 145
status: doing
priority: Normal
tags: [shearline, tarski, auth, explain, config-check]
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

## Slice 1: token scopes through Shearline (design note, 2026-10-26)

State when promoted: tarski v0.3.1 tagged and public; Shearline master requires it and its
CHANGELOG names v0.2.1, but no v0.2.1 tag exists on GitHub or the module proxy yet (only
v0.1.0, v0.2.0). `internal/auth/auth.go` is unchanged since 2026-05-24, so Shearline's
`examples/ductile` transcription and its parity test (154 tokens x 19 rows) are current.
Decided: adopt for the explainable 403 and the load-time refusal of unknown scopes, not for
the gate itself, which already works.

**Shape.**
- `internal/auth` keeps `Authenticate` and `Principal`; token matching is not policy.
- One route table in `internal/api` is the single source: `(method, path, action, capability,
  narrowest scope)`. `requireScopes(scopes...)` becomes `require(action, capability)` and the
  three in-handler checks (`handlePipelineTrigger`, write-class invoke in `handlePluginTrigger`,
  `canSeeJobResults`) become `Decide` calls on the same table.
- `normalizeScopes` becomes `Inherits` facts; `HasAnyScope` goes. The wildcard is a role that
  inherits the four `:rw` scopes.
- `buildRuntime` builds the `policy.Policy` from the resolved tokens next to `api.Config.Tokens`;
  reload swaps it like everything else. `Load` failing (a token holding a scope no route names)
  fails the build, as an unresolvable `secret_ref` does today.
- A 403 body gains `why` as the compact JSON `tarski.Absence`. The caller is already
  authenticated, so showing the grant table is acceptable for a personal gateway; note it in
  API_REFERENCE.
- Capability names end in `/` (`jobs/status/`) because grants are string prefixes with no
  path semantics; `jobs/status` would silently cover a later `jobs/status/history`.
- Scope-free endpoints and zero-scope tokens keep their current behaviour.
- Port `TestParityWithDuctile` in as ductile's own regression against the old table before the
  old code is deleted, then keep it as the pin on the route table.

**Out of this slice.** Webhook `scopes/*.json` (a second principal class) and the config-time
questions above, which import tarski directly; the go.mod diamond resolves to one tarski.

**Blocked by.** Was Shearline v0.2.1 on the proxy; tagged 2026-10-26 and in progress on
`auth/card145-shearline-scopes`. Names are contract (AGENTS.md section 2): the
action/capability vocabulary is in `internal/auth/policy.go` for review in the PR.

**Built (2026-10-26).** `internal/auth/policy.go`: the lattice, the capability table,
`NewPolicy`, `Decide`, `KnownScope`. `internal/api`: `require(action, capability)`, `decide`,
`writeForbidden`, routes name capabilities, `ErrorResponse.Why`. `config.validate` refuses an
unknown scope; `buildRuntime` builds the policy before the listener opens and names each
principal by its `secret_ref`. `TestPolicy_ParityWithScopeMap` holds the policy to the old
`normalizeScopes`/`HasAnyScope` on every scope, every pair and every route. One deviation from
the note above: the single source is a capability table (action, capability, narrowest scope)
and routes name a capability at registration, rather than a route table that registers routes;
same information, and the chi registration stays readable.

## History

Nothing here blocked the cut or its first tag. Ductile adopts after Shearline v0.2.1.
