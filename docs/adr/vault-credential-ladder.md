---
title: Vault credential ladder and two-posture bootstrap
status: Accepted
created: 2026-06-09
accepted: 2026-06-09
tags: [adr, vault, secrets, bootstrap, auth, api, trust]
relates_to: ["ADR credentialed-runtime-contract", "docs/SECRETS.md §3", "kanban #94 api-tokens-to-vault", "kanban #119 boot-refuses-unsafe-config", "kanban #128 vault-native-bootstrap"]
---

# ADR: Vault credential ladder and two-posture bootstrap

## Status

Accepted (2026-06-09). Settled in a design grilling session (operator + MacM1) that started from
"how does a from-scratch vault-native gateway bootstrap?" (#128) and resolved into the credential
model below. The model is a refinement/naming of what the code **already** implements — three
separate authenticators, a reserved admin-token with a single writer, fail-closed API boot (#94/#119).
This ADR makes that model canonical and decides the bootstrap shape. The precise transport for the
vault-operable posture (loopback/unix-socket vs. a non-wildcard `vault:admin` scope) is an
implementation follow-up under #128 and is **not** frozen here; the invariants that bound it are.

## Context

`#94` made API bearer tokens vault-only: `api.auth.tokens[].secret_ref` resolves against the vault
projection at load, **fail-closed**, before the listener opens (pinned by #119). That created an
apparent from-scratch deadlock (#128): the API token must be in the vault to boot, but the live
`vault set` writes go through the running daemon (sole writer) — which cannot boot until the token
exists. The investigation into "how to seed before first boot" surfaced three options (offline seed,
staged boot, genesis-mint). Rather than pick a seeding trick, the grilling re-derived **what the three
credentials in the system actually are**, and the bootstrap fell out of the model.

Three facts, verified in the code, anchor the model:

- The `/vault/*` management routes are gated by their **own** authenticator (`authenticateVaultAdmin`,
  `internal/api/server.go`), **separate** from the config API tokens (`s.authenticate` /
  `requireScopes`). An API token scoped `["*"]` reaches every gateway route but **cannot** write the
  vault — vault-write is a different plane, not a scope.
- The admin token is a **reserved** secret (`core-admin-token`, `internal/vault/reserved.go`). The
  generic data-plane `SetSecret` **refuses** reserved names (`internal/vault/lifecycle.go`) on *every*
  plane — online `vault set --api-url` and offline `vault set --vault --key` both funnel through it.
- The admin token has exactly **one writer**: `RotateAdminToken` (`internal/vault/lifecycle_owner.go`),
  used by both genesis (`vault init`) and `vault rotate-admin-token`. Both are **age-key, daemon-stopped**
  ops. The value is always **machine-minted** from a CSPRNG — never operator-chosen.

## Decision

### 1. The credential ladder (canonical model)

Three roles, three planes, each strictly scoped to its plane:

| Credential      | Verb                  | Plane                    | Cannot                                  |
|-----------------|-----------------------|--------------------------|-----------------------------------------|
| **vault key** (age) | **open** the vault    | offline, root of trust   | — (it is the floor)                     |
| **admin token**     | **operate** the vault | online `/vault/*`        | open the vault; operate ductile         |
| **api token**       | **operate** ductile   | online gateway           | operate the vault; open the vault       |

Each tier **issues the one below it** — a delegation chain:

```
vault key  ──mints──▶  admin token  ──mints──▶  api token
 (genesis,            (vault init /            (vault set,
  offline)             rotate-admin-token)      online)
  open vault           operate vault            operate ductile
```

### 2. Isolation is upward-blocking; the root subsumes all

A lower tier can never reach a higher one — an api token cannot operate the vault; an admin token
cannot open the raw blob or reach the key. But the **vault key subsumes everything** (it can decrypt
the admin token straight out of the blob). The ladder is `api ⊂ admin-reachable-state ⊂ key`, not three
equal compartments. This is correct for a root of trust.

### 3. The reserved credential rule (admin token is mint-only)

The admin token is **mint/roll-only, never `set`**:

- **Writer:** exactly one — `RotateAdminToken`. Reachable **only by the age key, only daemon-stopped**.
- **Value:** always machine-minted (CSPRNG); an operator/agent can never hand-pick or `set` it. The
  reserved guard blocks `set` on *every* plane, so there is no side door.
- Contrast with **api tokens**, which are ordinary secrets: settable to an operator/agent-chosen value
  via `vault set`, not reserved. "Bring your own value" applies to api tokens; the admin token is always
  born from the machine.

| | admin token | api token |
|---|---|---|
| Written by | `RotateAdminToken` only | generic `vault set` |
| Value | machine-minted (can't choose) | operator/agent-chosen |
| Reserved? | yes — `set` refused everywhere | no |
| Write authority | age key, daemon stopped | admin token, daemon running |

### 4. Bootstrap = the ladder, expressed as two postures

The from-scratch path is the delegation chain, not a special seeding trick. It maps to **two boot
postures over the three roles**:

- **post-genesis** — vault *open* (key present), vault *operable* (admin token live), ductile
  **closed** (no api token yet; the public gateway plane is **not** serving). Nothing unauthenticated
  is exposed: the only live surface is the admin-token-gated vault plane.
- **activated** — the admin token operates the vault to mint the api token(s); `system reload`; ductile
  *operable*.

This resolves #128 without an offline seed of the api token and without weakening #94: the api token is
**issued by the admin token** (the tier whose job is "operate the vault"), exactly as the ladder says.

### 5. Invariants that bound the implementation (non-negotiable)

The vault-operable / ductile-closed posture MUST be built so that:

- **The public gateway listener keeps its #94/#119 fail-closed gate.** It still refuses to open without a
  resolvable api token. The management posture is reached by *not yet serving the gateway plane*, never by
  opening the public listener unauthenticated.
- **No unauthenticated surface, ever.** Every mounted route is authenticated. The vault plane is reachable
  only with the admin token, bound to a **local** transport (loopback or unix socket + filesystem perms) —
  not the public network interface — so a same-host boundary, not just a network one, protects it.
- **The posture is first-class and observable**, consistent with the deployment-posture vocabulary
  (#111/#112) — not an accidental half-booted state a bug can strand.

## Implementation follow-up (resolved during #129)

The transport left open in the Status note is now decided:

- **Local transport = unix-domain socket (option a).** The management posture serves `/vault/*` on a
  unix socket (`api.management_socket`, default beside the state DB), created `0600` — a same-host
  *filesystem* boundary, exactly the invariant in §5. The loopback-mux (b) was rejected for admitting
  any local process; collapsing the two authenticators into a `vault:admin` scope (c) was rejected
  because it re-complects vault-write back into a *scope* when the design's premise (and the code,
  `internal/api/server.go`) is that vault-write is a separate **plane**, not a scope.
- **The api-enabled-needs-a-token invariant is now single-sourced.** It was triplicated across config
  load-validation, doctor, and runtime admission; #129 collapses it into one predicate
  (`config.APIEnabledWithoutToken(cfg, hasVault)`). Zero api tokens is rejected ONLY when no vault is
  present to bootstrap one — non-vault deploys stay fail-closed exactly as before.
- **Boot posture is a first-class value** (`config.BootPosture` / `DecideBootPosture`), satisfying the
  §5 "first-class and observable" invariant at the decision layer; surfacing it in `system status` /
  doctor / selfcheck is #130.
- **Offline `vault set --vault --key` (feat/128-vault-offline-seed): kept as a documented recovery
  tool, not the bootstrap path.** The bootstrap mints the api token through the admin token over the
  socket (proven end-to-end); the offline seed remains useful only for out-of-band recovery when the
  daemon cannot run, so it is retained but demoted in the docs (#131 reconciles them).

## Consequences

- **#128 re-scopes** from "implement an offline `vault set` seed" to "implement the vault-operable boot
  posture (admin-token-gated, local transport) + the activate-on-reload transition." The offline
  `vault set --vault --key` on `feat/128-vault-offline-seed` is **not** the chosen bootstrap mechanism;
  it may survive as a recovery tool or be dropped — decided during #128 implementation.
- The threat actor that picks this over a pure offline seed is the **compromised/prompt-injected agent
  holding the admin token**: the ladder keeps the *age key* (genesis authority) out of the agent's hands
  while still letting an admin-token holder run day-2 over the API. That separation of duties is the
  point. An admin-token compromise can mint api access (it always could — that is "operate the vault");
  it still cannot open the vault or rotate its own credential (age-key, offline only).
- `docs/SECRETS.md §3` gains a "Credential ladder" subsection stating the three roles; the existing
  sole-writer model and reserved-admin-token sections already encode the rules this ADR names.
