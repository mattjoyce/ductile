---
id: 115
status: todo
priority: Medium
tags: [test, dispatch, ci, flaky, tech-debt]
---

# Dispatch exec tests flake on `text file busy` (ETXTBSY) under CI parallelism

> **Nav:** surfaced 2026-06-08 while merging [[95-privsep-launchd-and-live-rollout]] (PR #121).
> Pre-existing on `main` — **not** caused by #95/#111. Prior stabilization attempt was
> commit `d6dc6c7` ("test(dispatch): stabilize temp plugin script execution"), which did
> not fully close the race.

## Symptom
`fast-validation → Run fast tests` reddens intermittently. Each run trips a **different**
exec-based test in `internal/dispatch/`, so re-running CI does not reliably converge.
Observed failures (PR #121 CI, two consecutive runs):

- `TestSpawnPluginTruncatesStderrWithoutFailingValidResponse` —
  `fork/exec .../plugin.sh: text file busy`
- `TestSpawnPluginFailsWhenStdoutExceedsLimit` —
  `fork/exec .../plugin.sh: text file busy`
- Locally a third, `TestSpawnPluginTimeoutKillsProcessGroup`, has also flaked
  (`read child pid: ... no such file or directory`).

All three pass cleanly **in isolation** (and 3× with `-count=3`), confirming a
concurrency/timing flake, not a logic bug.

## Root cause
Classic Go concurrent `fork`/`exec` ETXTBSY race (golang.org/issue/22315): while one
parallel test still holds a writable fd to *its* freshly-written `plugin.sh`, another
goroutine's `fork()` inherits that fd; the parent then `exec`s the script while a copy
of the writable fd is still open in the not-yet-`exec`'d child → `ETXTBSY`. O_CLOEXEC
does not close the window because the offending fd lives in a *different* test's forked
child between its own fork and exec. The many `t.Parallel()` exec tests in the package
make the window easy to hit on busy CI runners.

Helper of interest: `writeDispatchTestScript` in
`internal/dispatch/output_limit_test.go` (it already does O_EXCL + Sync + Close + chmod —
its own fd handling is fine; the race is cross-goroutine).

## Candidate fixes (pick after a quick spike)
1. **Bounded ETXTBSY retry at the exec site** — retry `cmd.Start()` a few times with a
   tiny backoff when `errors.Is(err, syscall.ETXTBSY)`. Most robust; also hardens
   production spawn against the same race when a plugin is written-then-run. Touches the
   dispatch spawn path — weigh as a real (small) behavior change, not just test code.
2. **Serialize the exec tests** — drop `t.Parallel()` on the script-exec tests (and/or a
   package-level exec mutex). Cheaper, test-only, but other parallel forks in the binary
   can still trigger it, so less complete than (1).
3. Combination: (2) to shrink the window + a small ETXTBSY retry guard in the test helper.

## Done when
- `go test ./internal/dispatch/ -count=20` is green locally, and
- `fast-validation` passes on CI across several consecutive runs without admin override.

## Notes
- PR #121 was merged via `--admin` over this flake (lint blocker was fixed separately in
  `d891f45`); this card tracks the remaining CI-reliability debt.
