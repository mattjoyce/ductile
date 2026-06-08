---
id: 116
status: todo
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
