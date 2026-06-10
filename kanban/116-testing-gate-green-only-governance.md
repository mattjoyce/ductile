---
id: 116
status: done
priority: High
tags: [testing, ci, governance, docker]
---

# Gate the system tier GREEN-ONLY (fix the silent-skip governance hole)

> **Nav:** from the 2026-06-08 luminary design council (Thompson×Feathers / Lamport×Pragmatic /
> Brooks×Beck) on the testing redesign. Sibling: [[117-queue-state-machine-invariant-suite]],
> [[118-system-tier-curate-trust-property-fixtures]], [[119-boot-refuses-unsafe-config]].

## Problem (the REAL defect — Brooks×Beck)
`docker-validation` has been **100% red since PR #94** (vault-only tokens) yet never blocked a
merge — it was silently skipped for weeks behind `fast-validation` failures (the now-fixed ETXTBSY
flake, #115). "A CI gate you never watched go green is the cardinal trust sin" (Thompson). A
mandatory-but-red tier trains everyone to skip it — which is exactly the failure we already have.

## Do
- The PR gate runs **only the fixtures that actually pass** — grow the green set, never gate on red.
- Make a red gated fixture **fail CI loudly** (no skip, no `|| true`), with artifacts.
- Supersede the speculative gate-expansion in PR #122 (it added `sync-terminal-route`, still broken
  by the literal-token drift) — only promote a fixture into the gate once it is green and migrated.

## Done when
- `docker-validation` is green on `main` and a deliberately-broken fixture turns the job red.
- No fixture is in the PR-gate list that doesn't boot.

## Notes
Highest-leverage, lowest-code step (Brooks×Beck). Needs the green set known — depends on the
fixture migration in [[118-system-tier-curate-trust-property-fixtures]] and a Docker host (Dell).

## DONE 2026-06-10 — gate is the directory; proven green AND red in real CI
`docker-validation` now runs `./scripts/test-docker` with NO fixture argument (commit `b5476a2`): the
runner enumerates `test/fixtures/docker/`, so the gate IS the curated tier — a fixture joins the gate
only by landing green in that directory. The dead names (api-e2e, scheduler-recovery — deleted in #118)
are gone from the workflow. No skip, no `|| true`; artifacts upload on failure.

**Evidence (workflow_dispatch on `feat/129-vault-operable-posture`):**
- GREEN: run 27234480869 @ `b5476a2` — fast-validation ✓ + docker-validation ✓ (all 7 fixtures green
  on the ubuntu runner — first linux validation of the new crash fixture; lint + sudo privsep suite
  also passed on the branch for the first time).
- RED: run 27234500694 @ `5add238` — a TEMPORARY deliberately-red `zz-broken-gate-probe` fixture turned
  docker-validation red AFTER all 7 real fixtures passed; `docker-test-artifacts` (126KB) uploaded.
  Probe reverted in `b8b84a8`.
The "green on main" half of done-when lands automatically when the branch merges — the same gate runs
on the push-to-main trigger. PR #122's speculative gate-expansion is superseded by this commit.

## Narrative
- 2026-06-10: Closed by making the gate self-maintaining rather than a hand-kept list — the silent-skip
  hole was a governance problem, so the fix removes the governance surface entirely (the directory is
  the gate). Proved both directions in real CI before calling it done: the tier passes, and a planted
  red fixture fails the job loudly with artifacts. (by @assistant)
