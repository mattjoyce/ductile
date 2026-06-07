---
id: 101
status: done
priority: High
blocked_by: []
tags: [privsep, dx, safety, v1.0]
---

# Privsep · make "valid config ≠ enforcing on this host" impossible to miss (v1.0 anti-footgun)

> **Nav:** [[83-privsep-epic]] · the **cheap NOW sliver** of the explain verb ([[99-privsep-explain-observable-posture]]).
> Closes the T7 trap. This is the *only* piece of the explain idea that earns a v1.0 slot.

**Job story:** *When* an operator or AI validates a config that is structurally valid but will run
**unconfined** on this host, *I want* the tooling to say so **loudly**, *so* nobody ships a gateway
believing it's walled when it isn't (the live-Mac T7 situation: vault loaded, no accounts / no
CAP_SETUID → `unconfined`, yet `config check` says "valid").

## Scope (small — off the existing pure functions, NOT the full explain verb)

- **Boot summary line:** at startup, log the privsep posture explicitly and unmissably —
  `enforce` / `unconfined` + the one-line why (truth-table cell). (Today the verdict is computed in
  `cmd/ductile/runtime.go:631-668` and only partially surfaced.)
- **`config check` warning:** emit a `doctor.Result` warning when the config is valid **but** the
  current host won't enforce — e.g. accounts configured without the drop capability, or a vault
  loaded while privsep resolves to `unconfined`. An AI reading `config check --json` then sees it in
  the structured findings, not just a human's terminal.

Explicitly **out of scope:** the per-plugin table, what-if, `--json` report object, `/system/doctor`
+ `/healthz` plumbing — those are the full [[99-privsep-explain-observable-posture]] (v1.x polish).

## Acceptance
- A "valid but unconfined" host says so at boot (one clear line) **and** in `config check` (a warning
  carried in `doctor.Result`, greppable / machine-readable).
- "valid" and "enforcing" are never conflated by silence again.
- No new privilege mechanism; reuses `evaluateBootGate` / `resolveAccount`.

## Narrative
- 2026-06-07: Carved out of #99 during the v1.0 triage as the minimal safety surface. The full explain
  verb family is comprehension/DX (later); this single anti-footgun is a correctness-of-operation
  concern (now), because the failure mode is shipping a wall that isn't there. (by @assistant)
- 2026-06-07: DONE — `config check`/doctor now WARNs (valid, not error) when secrets/vault are
  configured but no accounts map (T7 trap), and when `service.unconfined` sits on a configured
  accounts map; boot logs an explicit UNCONFINED posture line for the plain dev case. +2 tests. (by @assistant)
