---
id: 140
status: backlog
priority: Low
tags: [api, lifecycle, reload, supervision]
---

# Listener lifecycle asymmetries: gateway binds async (activation reload says "ok" before bind is known); StartManagement early errors leave serveDone open

> Raised 2026-06-10, dual luminary code review of `feat/129-vault-operable-posture`
> (Hickey×Armstrong F8 + F6, Lamport×Thomas/Hunt F5 — two halves of one supervision fix).

## The concerns (mirror-image defects)
1. **`Start` binds asynchronously** (`internal/api/server.go:178-198`): `ListenAndServe` runs in a
   goroutine, so a gateway bind error surfaces on `errCh` *after* the management→gateway activation
   `Reload` has returned `{Status: "ok"}` and committed `rm.runtime`. The reload-restore supervisor
   (`cmd/ductile/runtime.go:191-203`) can therefore never catch a gateway bind failure — the
   process exits 1 via `errCh` (loud, launchd restarts, not stranded) but the designed restore path
   is bypassed and the operator was told "ok". `StartManagement` does this half correctly
   (synchronous `net.Listen` before serving).
2. **`StartManagement` never closes `serveDone` on early errors** (`internal/api/management.go:44-69`):
   six returns before the serve goroutine (empty socket, nil vault, over-long path, stale-remove,
   listen, chmod failures) leave `serveDone` open forever; a subsequent `WaitListenersStopped`
   (`server.go:216-223`) burns the full 10s deadline and fails with a misleading
   "api listener stopped: context deadline exceeded". `Start` does this half correctly
   (`server.go:193-198` always closes it).

## Fix
Split bind from serve in `Start` (`net.Listen` synchronously, return the error; `Serve(ln)` in the
goroutine) so activation bind failures fail the reload and exercise the restore path. Close
`serveDone` on `StartManagement`'s pre-goroutine error returns (named-return defer guarded by
goroutine ownership). Each surface adopts the half the other already does right.

## Done when
An activation reload whose gateway bind fails returns an error (restore path exercised, no "ok");
a failed `StartManagement` followed by a reload does not stall 10s on a misleading timeout.
