---
id: 137
status: backlog
priority: Medium
tags: [api, unix-socket, security, hardening]
---

# Harden management-socket creation: typo'd path deletes an arbitrary file; perms window; unchecked parent dir

> Raised 2026-06-10, dual luminary code review of `feat/129-vault-operable-posture`
> (Hickey×Armstrong F2 + F7, Lamport×Thomas/Hunt F6 — three findings, one code locus).

## The concerns (all in `internal/api/management.go` socket setup)
1. **Unconditional remove** (`management.go:59-61`): `os.Remove(socket)` runs before bind with no
   check that the existing path *is* a socket. A typo'd `api.management_socket` pointing at any
   file the daemon can write (state DB, config, vault blob) is silently deleted at daemon
   privilege; the stale-socket rationale only holds for actual sockets.
2. **Perms window** (`management.go:62-69`): `net.Listen("unix", …)` creates the socket with
   umask-derived perms (typically 0755) and is accepting connections before `os.Chmod(…, 0o600)` —
   the documented "created owner-only" (`:40-42`) is briefly false, and unauthenticated `/healthz`
   (`:28`) rides the socket during the window.
3. **Hoped-for invariant** (`cmd/ductile/runtime.go:860-868`): the default location is asserted
   "beside the state DB (already a protected directory)" with no mode check anywhere; `MkdirAll`
   does not tighten a pre-existing looser dir (0700 only guaranteed when `storage.OpenSQLite`
   created it).

## Fix
`os.Lstat` first and only remove when `mode&os.ModeSocket != 0` — fail loud ("path exists and is
not a socket") otherwise. Create/verify a 0700 parent directory before binding (closes the perms
window and the unchecked default together); alternatively umask set/restore or bind-chmod-rename.

## Done when
A non-socket file at the configured path refuses boot with a typed error; the socket is never
observable with perms wider than 0600; the parent directory's mode is verified, not assumed.
