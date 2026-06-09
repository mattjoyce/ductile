---
id: 136
status: done
priority: Medium
tags: [runtime, posture, scheduler, dispatcher, secrets, fail-closed]
---

# Management posture: scheduler/dispatcher trigger planes aren't gated — "ductile-closed" still executes plugins

> Raised 2026-06-10, dual luminary code review of `feat/129-vault-operable-posture`
> (Hickey×Armstrong F3).

## The concern
Commit 46e4de8 closed the webhook plane in management posture ("part of 'ductile operable'…
exactly like the public gateway listener", `cmd/ductile/runtime.go:832-836`). But `sched.Start`
(`runtime.go:727`) and `disp.Start` (`:731-737`) run unconditionally before the posture branch, and
`secretComposer`/`pluginVerifier` are wired (`:620-626`) — so a bootstrap config that lands in
management posture (zero api tokens + vault) with schedules or watch triggers **fires pipelines and
delivers vault secrets** to attested plugins while the posture claims ductile is closed. The
`bootPosture` value is in scope at that point in the function; these planes just never consult it.

## Fix
Either gate `sched.Start`/`disp.Start` (or trigger admission) on
`bootPosture != PostureManagementOnly`, **or** — if internal trigger planes are deliberately
in-scope for management posture — record that in the credential-ladder ADR §4 and fix the
`runtime.go:832` comment that claims the webhook gate makes the posture "exactly like" the gateway
closure.

## Done when
A management-posture boot with a scheduled pipeline either does not fire it (gated), or the
ADR + comment explicitly state trigger planes stay open; one test asserts whichever is chosen.

## Narrative
- 2026-06-10: GATED (Matt's call over document-as-open): scheduler and dispatcher now start only
  when bootPosture != management-only, matching the webhook-plane precedent (46e4de8) — every plane
  that can fire a pipeline or deliver a vault secret is down until activation. Jobs enqueued during
  bootstrap stay queued and run after activation (deferred, not lost). Red→green: with the gate
  stashed, a queued probe job was picked up; with the gate, it stays `queued`. The runtime.go:832
  comment now names all gated planes. (by @assistant)
