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
