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

- [x] `startManagementServer`'s cleanup calls `client.CloseIdleConnections()` before `cancel()`, with a one-line comment naming the StateNew/Shutdown reason.
- [x] `git diff origin/main -- internal/api/management.go` is empty.
- [x] The 2s shutdown wait at management_test.go:85 is unchanged.
- [x] `go test -race -count=20 -run 'TestManagement|TestStartManagement' ./internal/api/` passes with zero failures (it fails on `main` today).
- [x] The gate passes.

## Gate

Repo-wide vet, `go test -race -shuffle=on ./...`, the 20x management run above, gosec medium on internal/api, and gofmt on the files you change. Pre-existing gofmt drift elsewhere is out of scope.

## Out of scope

- Formatting: run `gofmt -w internal/api/management_test.go` (it fixes the pre-existing import order there, which the gate checks because you touch the file). Format no other file.
- Pre-existing gosec G204 at internal/dispatch/sidedoor_audit_unix.go:82: leave it.
- Any other test or package.

Do not tag, do not merge, do not push.

## History

- 2026-10-27: card created; cause diagnosed from repeated failures on main.

## Review 1 (2026-10-27): approved

1. Diff checked against origin/main: `CloseIdleConnections()` before `cancel()` with the reason comment; import order gofmt'd; management.go untouched; 2s wait unchanged.
2. Gate rerun by the main loop on 371a45c: vet, full race suite, 20x management run, gosec on internal/api all pass.
3. Negative control: the same 20x run on unmodified origin/main fails 17 test cases; with the fix, 0 failures across 60 runs (40 in a separate scratch worktree, 20 in the gate).
4. Opencode session: ses_e822252e8ffe2I9f3EfTrFxMlp. The implement commit's subject does not follow `<component>: <action> <what>`; fix by squash at merge (suggested subject: `api: close idle test connections before management shutdown`).
