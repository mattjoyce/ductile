---
id: 117
status: doing
priority: High
tags: [testing, queue, state-machine, invariants, fast-tier]
---

# Queue state-machine invariant suite (the spine)

> **Nav:** luminary council 2026-06-08 — **Lamport × Pragmatic** lead. The fast-tier spine every
> later test slots into. Sibling: [[116-testing-gate-green-only-governance]],
> [[118-system-tier-curate-trust-property-fixtures]].

## Problem
Ductile *is* a state machine over a durable queue, but **the transition relation is not written down
anywhere** — and that absence is why the docker fixtures accreted one-per-feature. The scenario pile
asks "does feature X work?"; an invariant suite asks "is edge X→Y the *only* way there, and does it
*always* hold?". The first multiplies with features; the second is bounded by the state graph.

## Do — testing as checked invariant (in-process, temp SQLite, fast tier)
Encode the legal transition relation `queued → running → {succeeded, skipped, failed, timed_out, dead}`
as a table test, plus property tests for the named invariants:
- **No orphaned `running` survives restart** — `UpdateJobForRecovery` moves every `running` job to
  `queued` with `attempt+1` (`internal/queue/queue.go` ~`UpdateJobForRecovery`). Highest-value property.
- **Attempt is monotonic and bounded** — only increases; `failOrRetry` (`internal/dispatch/dispatcher.go`
  ~`failOrRetry`) enters `dead`/`failed` iff `AttemptsExhausted`; recovery counts as an attempt.
- **`timed_out` is reachable and distinct from `failed`** — SIGTERM→5s→SIGKILL lands `timed_out`.
- **Terminal states are absorbing** — no edge leaves `succeeded`/`dead`. Find unreachable states and
  ignored transitions (what cancels a `queued` job? other paths INTO `dead`?).
- **Fail-closed is total** — boot rejects literal token (#94, see [[119-boot-refuses-unsafe-config]])
  AND a missing vault secret fails the job closed, never delivers empty.

## Done when
- A `(from, event, to)` transition table exists as a checked artifact and the suite asserts it.
- Crash-recovery property test (enqueue → force-orphan `running` → recover → `queued`, `attempt+1`)
  passes in-process with no Docker.

## Progress
- 2026-06-08: **suite LANDED** in `internal/queue/statemachine_test.go` (PR #126). `legalTransitions`
  is the written-down `(from,event,to)` artifact; `TestQueueLegalTransitionsHoldInStore` drives every
  edge through the real store; `TestQueueTransitionRelationIsWellFormed` asserts reachable + absorbing
  + no dead-ends; `TestQueueCrashRecoveryRequeuesOrphanedRunning` proves orphaned `running` → `queued`,
  `attempt+1`, re-claimable; `timed_out` distinct from `failed`; `Complete` rejects non-terminal target.
  In-process, temp SQLite, no Docker. Attempt-bounding *decision* stays covered by existing
  `internal/dispatch/retry_policy_test.go` (not duplicated). Merge when #126 fast-validation green.

## Notes
Tracer bullet for the whole redesign (Lamport): `internal/queue/statemachine_test.go`. When green,
you *know why* it's green — one honest gate worth more than 18 red fixtures.
