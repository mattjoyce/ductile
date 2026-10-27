---
id: 146
status: doing
priority: Normal
tags: [test, flake, api, management]
---

# Management-socket tests flake: server never finishes shutting down

## Problem as it is now

`internal/api/management_test.go` fails intermittently on `main` under
`go test -race -count=1 ./internal/api/`: in roughly a third of runs one of
`TestManagementPostureDoesNotServeGatewayPlane`, `TestStartManagementSocketIsOwnerOnly`,
`TestManagementHealthzReportsPosture` fails at ~2.06s with

    management_test.go:86: management server did not shut down

The cleanup returned by `startManagementServer` (management_test.go:80-88) cancels the
server's context and waits 2s for `StartManagement` to return.

## Cause (diagnosed; do not re-decide)

`StartManagement` (internal/api/management.go) shuts down with `http.Server.Shutdown`.
Shutdown closes idle connections promptly, but net/http treats a connection still in
`StateNew` (accepted, no request read yet) as idle only once it is about 5 seconds old.
The test client's `http.Transport` keeps connections alive and can dial a spare connection
during the readiness poll loop (management_test.go:66-78) that never carries a request.
That connection sits in `StateNew` on the server, so Shutdown blocks ~5s, past the test's 2s
wait. The production code is correct; the test leaves a client connection open.

## Change

Test-only. In `startManagementServer`'s returned cleanup, close the client's idle
connections (`client.CloseIdleConnections()`) before calling `cancel()`, so the server sees
the spare connection close and Shutdown returns at once. Do not change
`internal/api/management.go`. Do not raise the 2s wait. Do not disable keep-alives (that
hides the cause rather than releasing the connection the test opened).

## Acceptance

- [ ] `startManagementServer`'s cleanup calls `client.CloseIdleConnections()` before `cancel()`, with a one-line comment naming the StateNew/Shutdown reason.
- [ ] `git diff origin/main -- internal/api/management.go` is empty.
- [ ] The 2s shutdown wait at management_test.go:85 is unchanged.
- [ ] `go test -race -count=20 -run 'TestManagement|TestStartManagement' ./internal/api/` passes with zero failures (it fails on `main` today).
- [ ] The gate passes.

## Gate

`~/.claude/skills/_POINTDEV/Tools/GateGo.sh` (gofmt, vet, `go test -race -shuffle=on ./...`, gosec).

## Out of scope

- Pre-existing goimports drift in `internal/api/management_test.go` (import order): leave it.
- Any other test or package.

Do not tag, do not merge, do not push.

## History

- 2026-10-27: card created; cause diagnosed from repeated failures on main.
