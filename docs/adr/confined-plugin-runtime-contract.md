---
title: Confined-plugin runtime contract
status: Accepted
created: 2026-06-07
accepted: 2026-06-07
tags: [adr, privsep, plugins, runtime, enforce]
relates_to: ["ADR filesystem-layout", "PrivSec and Secrets ADR (§3 Layer 1b, §5, §8)", "kanban #83 privsep epic", "kanban #109 uv-shebang plugins under enforce", "kanban #107 plugins vault-native", "docs/PLUGIN_DEVELOPMENT.md"]
---

# ADR: Confined-plugin runtime contract

## Status

Accepted (2026-06-07). The design was settled in the privsep war-room and the gateway-side
implementation (#109 "C") landed on `feat/privsep-uid-separation`: the `0711` traversal floor in
`deploy/systemd/ductile-accounts.tmpfiles.conf` plus `HOME`/`XDG_CACHE_HOME`/cwd rebasing in
`internal/dispatch/{env,subprocess_executor}.go`. The on-box 4-point verification (boot gate,
secret-file unreadability, state_dir write, cross-account isolation) is owned by the Thinkpad enforce
instance and tracked on card #109.

## Context

Privsep (PrivSec ADR Layer 1b) drops each plugin from the gateway uid to a distinct unprivileged
**account** uid at spawn. That drop silently rewrote the **runtime contract** a plugin runs under, and
nothing wrote the new contract down — so plugins kept assuming the *old* one and failed closed one
runtime at a time (#109).

Pre-enforce, a plugin inherited the gateway's environment: a writable `$HOME`, a usable cwd, `/tmp`,
and the gateway's whole ambient world. Under enforce, a dropped account got **none** of that:

- `HOME` and cwd were still the gateway's `/var/lib/ductile` — `0700 ductile`, so **not writable or
  even readable** by the account uid. Anything keyed to `$HOME` (a uv/pip cache, `__pycache__`, a
  dotfile) hit permission-denied.
- The account's OWN `state_dir` (`/var/lib/ductile/accounts/<name>`, `0700`, account-owned) was
  **unreachable by path**: the `0700` on the parent `/var/lib/ductile` blocked the account uid from
  `x`-traversing down to it. Setting `cmd.Dir` could not fix this either — Go applies the setuid drop
  *before* `chdir`, so the `chdir` ran as the account and hit the same wall.
- The only writable absolute path a dropped account had was host `/tmp`.

This was never a uv quirk. `uv` tripped first because it needs a writable cache, but the same wall
breaks every state-writing plugin — proven by `github_repo_sync`'s `mkdir` into its state_dir. The
per-account `state_dir` feature was effectively half-dead: only event-emitting plugins survived,
because they never touched the filesystem.

A shared writable cache (one cache base several account uids could write) was considered and
**rejected**: it is a cross-account code-execution vector — a popped `untrusted` plugin writes a
malicious wheel/bytecode that an enforced `default` plugin then executes, defeating the wall. The cache
must be **per-account**, which means it must live under each account's own `state_dir`.

## Decision

Define and guarantee a runtime contract that every confined plugin can rely on, rooted entirely in the
account's own `state_dir`. The gateway provides it; the plugin author may assume exactly it and nothing
more.

**A confined plugin is guaranteed, at spawn:**

1. A **writable private `HOME`** = the account's `state_dir`. It is `0700`, owned by the account uid,
   and shared with no other account.
2. A **writable cache** = `XDG_CACHE_HOME` set to the same `state_dir` (so `uv` → `$XDG_CACHE_HOME/uv`,
   and any XDG-respecting runtime, lands in the account's private dir).
3. A **writable working directory** = the same `state_dir` (`cmd.Dir`), reachable now that the state
   root is `0711` (traverse-only) so the dropped uid can `x` its way down to the `0700` dir it owns.
4. **Secrets over stdin only** — never the environment, never argv (the gateway withholds its whole
   environment and delivers granted secrets in the request's `secrets` field; see the Secrets ADR).
5. A writable `/tmp` (shared host tmp; no `PrivateTmp` today).

**A confined plugin must NOT assume:**

- any ambient `$HOME` dotfiles, `/home/<user>` paths, or host-user config;
- the ability to write **anywhere outside its own `state_dir`** (and `/tmp`) — sibling account dirs,
  the gateway's state root, system paths all fail closed;
- network-fetched dependencies resolved **at spawn** as the default path (see Consequences);
- any host-ambient credentials, tokens, or environment beyond the minimal allowlist.

### Mechanism

- **`0711` traversal floor** (`ductile-accounts.tmpfiles.conf`): `/var/lib/ductile` and
  `/var/lib/ductile/accounts` are `0711` (traverse-only, NOT listable). Per-account dirs stay `0700`,
  account-owned. This is the standard `/home` pattern: an account reaches the dir it owns, cannot list
  or read siblings, and the gateway's secret files inside (`vault.age`, `ductile.db`) stay protected by
  their own `0600` modes — **traverse ≠ read**. The boot fs-reconcile gate tightens the db *file* to
  `0600` and never touches these directory modes, so `0711` survives every boot (the only residual leak
  is `stat`-of-size on a `0600` file, acceptable under the popped-plugin threat model).
- **Runtime rebasing at spawn** (`subprocess_executor` + `withAccountRuntimeEnv`): for a confined
  account with a `state_dir`, the gateway drops any inherited `HOME`/`XDG_CACHE_HOME` (so its own home
  never leaks to the child) and re-points both at the `state_dir`, and sets `cmd.Dir` to the same.
  Unconfined plugins are untouched — gateway uid, gateway `HOME`/cwd, exactly as before.

## Consequences

**Simple plugins stay simple — or get simpler.** A stdlib `python3` plugin that reads JSON on stdin,
writes JSON on stdout, and writes any files under its own cwd/`HOME` needs **zero** privsep-awareness;
the contract above is what makes that just work. Pre-C even this was quietly broken (the inherited
`HOME` was unwritable), so the contract is a fix, not a new tax.

**`uv`-inline-dependency plugins move from blessed-default to advanced.** Post-C they *work* (the cache
lands in the per-account `state_dir`), but they pay a cold dependency resolve per spawn and carry the
sharper edge that made the shared-cache option a security hazard. The blessed default exemplar becomes
**stdlib / system-runtime**; `#!/usr/bin/env -S uv run --script` is an advanced tier for plugins that
genuinely need a third-party library, documented with the isolation caveat.

**A plugin owns only its own job.** The contract makes "write only under your own `state_dir`" a hard
boundary, which surfaces responsibility leaks as failures — e.g. a discovery plugin pre-creating a
*downstream* plugin's clone dir now fails closed, correctly. Exemplars are to be swept for this.

**Exemplars must be rewritten** to the contract (tracked on #109 / the exemplar re-tier): stdlib-first,
no spawn-time dep fetch in the common case, and no reach outside the plugin's own `state_dir`. The
plugin code lives in the separate `ductile-plugins` repo; this ADR is the spec that rewrite conforms to.
The working reference is `plugins/_template/` (core repo) — the copy-me Tier-1 exemplar.

## The plugin tiers (decision rule)

The contract is runtime-neutral; what differs is *how a plugin gets its dependencies*. Three tiers,
in order of preference:

| Tier | Choose when | Pattern | Trade-off |
|---|---|---|---|
| **1 — stdlib (default)** | anything with real logic or that emits structured events | `#!/usr/bin/env python3`, stdlib only, vendor `_lib/` helpers; pre-built/bundled node counts too | none — fetches nothing, keeps the full structured protocol |
| **2 — `sys_exec`** | the job genuinely *is* "run a stable system command" (build, sync, scheduled maintenance) and exit-code/output is sufficient | the bundled `sys_exec` plugin, command in operator config | loses structured events/typed output; it is the widest exec surface, so use it *narrowly*, not as a default |
| **3 — fetch-at-spawn (advanced)** | a third-party library is genuinely unavoidable and cannot be vendored or pre-built | `uv run --script` (py) or a bundled artifact (node); cache lands per-account under `state_dir` | cold resolve per spawn; spawn-time code execution from a registry (`postinstall`) — prefer build-ahead |

`sys_exec` becomes *more* attractive under the contract (zero-friction, no spawn-fetch) but is **not**
the de-facto default: it trades away the structured event model ductile exists to provide and is the
broadest security surface — exactly what privsep is narrowing. The contract's lesson is "don't fetch
deps at spawn," which Tier 1 satisfies *while keeping* the protocol. Prefer Tier 1; use Tier 2 for true
shell-out jobs; reach for Tier 3 only when forced, and build ahead rather than fetch at spawn.
