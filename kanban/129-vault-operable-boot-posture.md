---
id: 129
status: todo
priority: High
tags: [vault, bootstrap, api, boot, posture, security]
---

# Implement the vault-operable boot posture (the real #128 fix)

> **Nav:** child of [[128-vault-native-bootstrap-no-offline-seed]]; design root is
> [[../docs/adr/vault-credential-ladder]] (`docs/adr/vault-credential-ladder.md`).
> Siblings: [[130-activate-on-reload-observable-posture]], [[131-from-scratch-bootstrap-acceptance-test]].

## Problem
A from-scratch vault-native gateway can't boot with the API enabled: the api token must be in the vault
at load (#94, fail-closed, pinned by #119), but the live `vault set` writes go through the running
daemon — which can't boot until the token exists. The agreed resolution is **not** an offline seed; it
is the credential ladder walked through two postures (ADR). This card builds the first posture.

## The posture
Boot **vault-operable / ductile-closed**: the daemon serves the admin-token-gated `/vault/*` management
surface on a **local transport**, even when `api.auth.tokens` is unresolved/empty — **without** opening
the public gateway listener. Then the admin token mints the api token; activation is [[130]].

## DECIDE FIRST — the local transport (ADR left this open)
Pick one and record the rationale in the ADR:
- **(a) Unix socket** for `/vault/*` — a second listener on a perm-gated unix socket, always available
  regardless of `api.enabled`. Strongest isolation (same-host filesystem boundary, not just network).
  Cost: new listener + CLI must target it (`--api-url unix://…` or a `--socket` flag in `vault.go`).
  **(recommended — matches the ADR "local transport, same-host boundary" invariant).**
- **(b) Loopback management-only mux** — boot a 127.0.0.1 listener that mounts ONLY the `/vault/*` group
  pre-activation; mount the gateway plane only after activation. Simpler (reuses TCP), weaker isolation
  (any local process can reach loopback).
- **(c) Non-wildcard `vault:admin` scope** — collapse the two authenticators: the admin token becomes a
  minimal `vault:admin`-scoped api token (a scope the wildcard `*` does NOT cover), so the #94 gate is
  satisfied by it and the daemon comes up with only the vault plane reachable. Most elegant, most
  invasive to the auth model.

## Do
1. Resolve the transport decision above; update the ADR's "implementation follow-up" note.
2. Rework the boot seam at `cmd/ductile/runtime.go:725` so the management surface can come up while
   `api.auth.tokens` is unresolved — **without** opening the public gateway listener.
3. Keep `authenticateVaultAdmin` as the gate for `/vault/*` (it already is, `internal/api/server.go:298`).
4. If (a)/(b): teach the `vault set`/`register-principal` CLI to target the local management transport.
5. Decide the fate of the offline `vault set --vault --key` on `feat/128-vault-offline-seed`: keep as a
   documented recovery tool, or drop. Note the decision here.

## HARD INVARIANTS (do not regress)
- The **public gateway listener keeps its #94/#119 fail-closed gate** — it still refuses to open without
  a resolvable api token. The posture is reached by *not yet serving the gateway plane*, never by opening
  the public listener unauthenticated.
- **No unauthenticated surface, ever.** Every mounted route requires the admin token; the management
  transport is **local** (loopback/unix-socket + fs perms), not the public interface.
- Genesis (`vault init`) and the reserved admin-token rules are untouched (mint-only, one writer).

## Done when
From a genesis-only vault (admin token seeded, NO api token), the daemon boots, `/vault/*` answers with
the admin token over the chosen local transport, and the **public gateway listener is not open**. The
api token can then be minted through it (proven end-to-end in [[131]]).
