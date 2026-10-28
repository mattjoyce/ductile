---
id: 149
status: backlog
priority: Normal
tags: [testing, flake]
---

# Three tests that fail under `-race` and machine load

> Found 2026-10-28 when two pointdev gate runs (`go test -race -shuffle=on -count=1 ./...`)
> ran at once for card 148. opencode fixed all three to get its gate green; the fixes were out of
> scope there and were dropped from that branch. They are recorded here so the work is not lost.

1. `internal/dispatch/dispatcher_test.go` `TestDispatcher_Start_ParallelExecution` asserts
   parallelism by wall clock (3 x 1s jobs within 2.5s). It measured 2.2 to 2.5s alone and 4.4s
   under load. Proposed fix: prove concurrency structurally. Each plugin instance touches a marker
   in a shared dir and waits (bounded, ~6s) until three markers exist, then succeeds; serial
   dispatch can never get three successes. Drop the elapsed-time threshold. Check before merge:
   the test's `max_workers` must be at least 3 regardless of host CPU count.
2. `internal/dispatch/process_unix_test.go` `TestSpawnPluginTimeoutKillsProcessGroup` uses a 1s
   spawn timeout; under load the child has not written `child.pid` before the group is killed.
   Proposed fix: 5s.
3. `internal/api/integration_test.go:328` `TestSynchronousPipelineSkippedEntryResponseVsDB`
   hardcodes `localhost:18081`; a concurrent run bound the same port and answered the request
   (500). Proposed fix: the file's existing `allocateLocalAddr(t)` helper. Sweep for other
   hardcoded ports (`scripts/test-fast` already warns about 18080).

## Done

- No wall-clock parallelism assert, no fixed test ports in `internal/api`.
- Two concurrent `go test -race -shuffle=on -count=1 ./...` runs in separate worktrees both pass,
  three times.

## History

- 2026-10-28: carded from card 148's slice 0 round.
