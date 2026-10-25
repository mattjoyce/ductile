---
id: 120
status: review
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

## Progress
- 2026-06-08 (PR #126): **euid guard DONE.** `requireWritablePermsEnforced`
  (`internal/vault/preconditions_test.go`) `t.Skip`s the two chmod-0500 persist-failure tests under
  euid 0 with the assumption named; TESTING.md §2 now points at it instead of describing folklore.
- **Remaining:** the `bash`/`python3` named-skip guards on the `internal/e2e` / `internal/dispatch`
  exec tests. (Both binaries exist on the Mac + Dell, so these don't false-fail here yet — lower
  priority; do when curating the system tier or hardening CI images.)
- 2026-10-25 (branch test/card120-interpreter-preconditions): **interpreter guard DONE.**
  `requireScriptInterpreter` (`internal/dispatch` and `internal/e2e` `preconditions_test.go`)
  reads a script's shebang and skips, naming the interpreter, when it is missing (absolute
  path or `/usr/bin/env X`, flags after env tolerated). Called from the script writers
  (`createTestPluginWithManifestExtra`, `writeDispatchTestScript`, e2e `createPlugin`) and
  for the bundled echo and sys_exec entrypoints. A table test pins skip-only-when-missing.
  TESTING.md §2 now points at the two in-code guards; `scripts/test-fast` NOTEs predicted
  skips instead of warning of failures. The privsep tests write `/bin/sh` scripts and are
  already root-gated, so they are left alone.
- Verified: non-root on the Mac, `go test -race -shuffle=on` green for dispatch + e2e;
  golangci-lint and gosec raise nothing on the changed lines. **Not verified:** the
  root-without-bash/python3 run (no Docker on the Mac); one `golang:alpine` run as root
  closes the Done-when.
- Seen on the way, out of scope: gofmt drift in `internal/api/management_test.go` and
  `internal/dispatch/sidedoor_audit_test.go`; 3 errcheck hits in `dispatcher_test.go`;
  local gosec build too old for Go 1.25 export data ("could not import").
