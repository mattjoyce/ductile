---
id: 150
status: backlog
priority: Normal
tags: [shearline, auth, config-check, observability]
depends_on: [145]
---

# `config check` reports the token policy's cost margin

> Shearline v0.4.0 gives every `Decision` its evaluation cost (`Steps`, `Bytes`) and a policy its
> bounds (`Policy.Bounds()`). Ductile requires v0.4.0 (card 145) but reads neither. Use them so an
> agent can ask, at load time, how close the token policy is to its evaluation bounds.

## Change

- At `config check` and at boot (where card 145 builds the policy), decide every route
  capability against every scope the tokens hold, once. Record the worst `Steps` and `Bytes`.
- Report the margin against `Bounds()`: `config check` prints one line, e.g.
  `token policy: worst decision 412/100000 steps, 9 KiB/4 MiB (0.4%, 0.2%)`; boot logs it once at
  info.
- Refuse past a fixed margin (proposal: 50% of either bound), with an error naming the route and
  scope that cost most. A policy that close to its bounds is a design problem, not a runtime one.
- A `*TooLarge` from any of these decisions is a hard refusal (cost is on the error via `errors.As`).
- `system status` / explain output carries the same two numbers, so an agent can read the margin
  from a running gateway without re-deciding.

## Notes

- The policy is tiny (a handful of roles, ~20 capabilities), so the sweep is cheap; measure it and
  write the number here.
- Cost is "for observation, in no hash, and may change with any tarski version" (Shearline
  CHANGELOG v0.4.0): never compare it across versions, only against `Bounds()` of the same build.
- v0.3.0 consumer `Relations` stay unused: the scope lattice fits `inherits`/`grant`.

## Done

- `config check` prints the margin line; a test policy past the margin is refused with the route
  and scope named; boot logs the margin; status exposes it.
- Merge-grade gates in AGENTS.md section 9.

## History

- 2026-10-28: carded; principal kept Shearline v0.4.0 on card 145 to use its decision cost.

## Build spec (2026-10-28, for opencode; design decided, do not redesign)

Card 145 is merged (`674d7d1`). Shearline v0.4.0: `policy.Decision` has `Steps`, `Bytes`;
`policy.Policy.Bounds()` returns `policy.Bounds{MaxSteps, MaxBytes int}`.
**`system status` exposure is deferred to a follow-up; do not touch status.**

### A. `internal/auth/policy.go`

1. `Decision` gains `Steps, Bytes int` (doc: "the evaluation cost Shearline reports; zero on
   error; for observation only, never compared across builds"). `Decide` copies `d.Steps`, `d.Bytes`.
2. `Policy` gains an unexported `principals []string` (each token's `principalName`, in token order),
   filled by `NewPolicy`.
3. New exported type and constant:
   ```go
   // MarginLimit is the share of either evaluation bound a token policy may use
   // at its costliest decision. A policy past it is a design problem, refused at
   // load rather than discovered under load.
   const MarginLimit = 0.5

   // Margin is a token policy's costliest decision against its evaluation bounds.
   type Margin struct {
   	Decisions          int    // decisions swept: principals x capability-table rows
   	Steps, Bytes       int    // worst of each, over every decision
   	MaxSteps, MaxBytes int    // Policy bounds
   	Principal, Action, Capability string // the decision with the most steps
   }
   ```
4. `func (pol Policy) Margin(ctx context.Context) (Margin, error)`: for every principal in
   `pol.principals` and every row of `grants` (its `action`, `capability`), call `pol.Decide`. Any
   error returns `Margin{}` and `fmt.Errorf("auth: policy margin: %w", err)` (a `*TooLarge` surfaces
   this way). Track the maximum `Steps` (with its principal/action/capability) and the maximum `Bytes`
   independently. A zero Policy returns `ErrNoPolicy`.
5. `func (m Margin) String() string`, exactly this format:
   `token policy: worst decision %d/%d steps (%.1f%%), %d/%d bytes (%.1f%%) over %d decisions; costliest: %s %s for %s`.
   With `Decisions == 0`: `token policy: no tokens, nothing to decide`.
6. `var ErrPolicyMargin = errors.New("auth: token policy past its cost margin")` and
   `func (m Margin) Check() error`: if `Steps > MarginLimit*MaxSteps` or `Bytes > MarginLimit*MaxBytes`
   return `fmt.Errorf("%w (limit %.0f%% of either bound): %s", ErrPolicyMargin, MarginLimit*100, m)`;
   else nil.

### B. Boot (`cmd/ductile/runtime.go`, right after `auth.NewPolicy(tokens)` near line 891)

`m, err := apiPolicy.Margin(ctx)` (use the context already in scope; if none, `context.Background()`);
on error, or if `m.Check()` errors, log it at error and return
`fmt.Errorf("api policy margin: %w", err)` exactly like the NewPolicy failure above it. Otherwise
`logger.Info("API policy cost margin", "decisions", m.Decisions, "steps", m.Steps, "max_steps", m.MaxSteps, "bytes", m.Bytes, "max_bytes", m.MaxBytes, "costliest", m.Action+" "+m.Capability)`.

### C. `config check` (`cmd/ductile/config.go`, `runConfigCheck`, after `result := doc.Validate()`)

1. `internal/doctor/doctor.go` `Result` gains `PolicyMargin string \`json:"policy_margin,omitempty"\``;
   `FormatHuman` prints `r.PolicyMargin + "\n"` as its last line when non-empty.
2. In `runConfigCheck`: when `cfg.API.Enabled` and the config has tokens, build
   `auth.NewPolicy` from `cfg.API.Auth.Tokens` mapped to `auth.TokenConfig{Name: t.SecretRef, Scopes: t.Scopes}`
   (no secret is resolved; the policy needs names and scopes only). Then `Margin`, then `Check`.
   Any error is a doctor error, category `api_policy`, field `api.auth.tokens`, and sets
   `result.Valid = false`. On success set `result.PolicyMargin = m.String()`.
   Add the error with the same helper or shape `appendIntegrityFindings` uses.

### D. Tests (beside the code, real policy, no mocks)

1. `internal/auth`: `TestPolicyMargin`: tokens `[{Name:"admin",Scopes:["*"]},{Name:"status",Scopes:["jobs:status:ro"]}]`.
   Assert `Decisions == 2*len(grants)`, `Steps > 0`, `Bytes > 0`, `MaxSteps`/`MaxBytes` equal the
   Shearline bounds, `Check()` is nil, `String()` starts `token policy: worst decision`.
   `t.Logf` the `String()` so the measured number is in the test output.
2. `TestMarginCheckRefusesPastLimit`: a hand-built `Margin{Decisions:1, Steps: 60, MaxSteps: 100, Bytes: 1, MaxBytes: 100}`
   -> `errors.Is(m.Check(), ErrPolicyMargin)`; same for Bytes 60/100; 50/100 on both -> nil.
3. `TestPolicyMarginNoTokens`: `NewPolicy(nil)` -> `Decisions == 0`, `Check()` nil, the "no tokens" string.
   Zero `Policy{}` -> `errors.Is(err, ErrNoPolicy)`.
4. `TestDecideReportsCost`: one allowed and one denied decision both have `Steps > 0`.
5. `cmd/ductile`: a `config check` test (follow `config_lock_migration_test.go`'s
   `captureOutputWithExitCode(t, func() int { return runConfigCheck(...) })`) on a config with the API
   enabled and one token: exit 0 and stdout contains `token policy: worst decision`. Reuse an existing
   cmd test fixture that already has an API token; say which one you used.

### E. Docs

`docs/CONFIG_REFERENCE.md`, in the section that documents `api.auth.tokens` scopes, add exactly:

```
`config check` also reports the token policy's cost: every token is decided against every
capability once, and the costliest decision is printed against the policy engine's bounds
(`token policy: worst decision ...`). A policy whose costliest decision uses more than half of
either bound is refused at `config check` and at boot.
```

No CHANGELOG edit (the release entry is written separately).

## Acceptance

- [ ] A-E done as written; no change to `system status` or to any allow/deny.
- [ ] Tests D1-D5 pass under `-race`; report the `TestPolicyMargin` log line in your summary.
- [ ] The gate passes. The three flaky tests on card 149 are not edited; rerun on a flake.

Do not tag, do not merge.
