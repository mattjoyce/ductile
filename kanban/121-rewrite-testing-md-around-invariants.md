---
id: 121
status: todo
priority: Normal
tags: [testing, docs, naur-procida]
---

# Rewrite TESTING.md around the transition table + green-gate model (descriptive, not aspirational)

> **Nav:** luminary council 2026-06-08 — do this **LAST**, as a Naur×Procida reconciliation pass once
> the redesign has landed. Sibling: [[117]], [[118]], [[116]].

## Problem
docs/TESTING.md is a target-state design doc heavy on "should/recommended/intended" that drifted from
reality (it hand-listed 8 fixtures while 18 existed; called `pipeline-level-if` "deferred" though it
ran). Naur: a doc that describes surface mechanics already drifted from the theory misleads more than it
helps. §1 (orchestration stays in `scripts/`) and §2 (env contract) are sound; the rest needs rewriting
from what the code/scripts ACTUALLY do.

## Do (after the redesign lands)
- Lead with the **theory**: Ductile is a state machine over a durable queue; tests are checked
  invariants + trust-property witnesses, not feature scenarios. Make the transition table ([[117]]) the
  spine of the doc.
- Procida separation — split the one-big-doc into: tutorial (run the suite in 60s), how-to (add a test
  at the right tier), reference (the canonical scripts + the fixture index), explanation (why the tiers,
  why green-only gating, why not test every CLI invoke).
- Record the decisions: keep the fast suite; system tier = live-only trust properties; **don't test
  every CLI invocation** (test the contract — honest exit codes, stdout/stderr separation, boot-refusal,
  auth-required — not every flag permutation); gate green-only.

## Done when
Every command/flag/path in TESTING.md runs today; the doc carries enough theory to revive the strategy
if all else were lost (Naur's test); no page is two Procida-types at once.

## Notes
Supersedes the interim tier edits in PR #122 (fold/replace). The fixture index lives in
`test/fixtures/docker/README.md`.
