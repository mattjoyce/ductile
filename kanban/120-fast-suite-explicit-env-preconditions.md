---
id: 120
status: todo
priority: Normal
tags: [testing, fast-tier, preconditions, privsep]
---

# Make the fast suite's env preconditions explicit (not folklore)

> **Nav:** luminary council 2026-06-08 — all three pairs agreed. The fast suite is KEEP; its two
> "flakes" were signal, not rot. Sibling: [[117-queue-state-machine-invariant-suite]].

## Problem
The fast suite couples to host environment in a few places, documented only as prose in TESTING.md §2:
- `internal/vault` tests force a persist failure with `chmod 0500` and assert the write is rejected —
  **root bypasses filesystem permissions**, so they fail as "expected a save failure" under uid 0
  (a stock CI container / bare `golang` image). This is the test *correctly* detecting that the privsep
  threat model evaporates under the wrong account — that's signal.
- `internal/e2e` / `internal/dispatch` plugin-spawn tests exec real entrypoints needing `bash`/`python3`.

The assumption ("filesystem permissions are enforced", "bash/python3 present") is **silent** — make it
an explicit, named precondition (Lamport: state the assumption or skip with it named).

## Do
- Guard the perm-injection tests with a euid check: `t.Skip` when `os.Geteuid()==0`, **skip reason names
  the assumption** ("requires non-root: root bypasses chmod-0500 persist-failure injection").
- Guard the exec tests on `bash`/`python3` presence with a named skip (a skip is never a pass).
- Reduce TESTING.md §2 to a pointer at these in-code preconditions rather than folklore.

## Done when
`go test ./...` is green as root AND non-root (skips, never false failures), each skip naming its
assumption. The ETXTBSY flake is already handled separately (#115 / PR #123).
