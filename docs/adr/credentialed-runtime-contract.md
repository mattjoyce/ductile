---
title: Credentialed (trusted-tier) runtime contract
status: Accepted
created: 2026-06-08
accepted: 2026-06-08
tags: [adr, privsep, plugins, runtime, trusted, credentialed]
relates_to: ["ADR confined-plugin-runtime-contract", "ADR filesystem-layout", "PrivSec and Secrets ADR (§4/§5)", "kanban #111 trust-tier", "kanban #83 privsep epic"]
---

# ADR: Credentialed (trusted-tier) runtime contract

## Status

Accepted (2026-06-08). The gateway flavour landed on `feat/privsep-uid-separation` and was proven
end-to-end on the Dell: a **cap-only** gateway (CAP_SETUID/SETGID, no root) drops a trusted plugin to a
real login uid with `HOME=/home/<user>`, reaching that user's on-disk creds via `~`, while a confined
plugin on the same gateway stays walled and cannot read secret zero. Design agreed in the privsep
war-room (operator + ThinkPad-ductile-admin + MacM1).

## Context

The confined runtime contract walls a plugin off: it drops to a throwaway account and its
`HOME`/`XDG_CACHE_HOME`/cwd are rebased to a private `state_dir`. That is correct for the standard and
untrusted tiers. But a homelab has a small set of plugins that must **act as the operator** — push to
git with the operator's ssh key, use their `gh` token — and reaching those on-disk creds (`~/.ssh`,
`~/.config/gh`) requires the plugin's `HOME` to be the operator's *real* home, not a walled state_dir.

The operator's framing settled the naming: this set is **trusted**, not "difficult". A plugin is in
this tier because the operator vouches for its code and accepts that *its compromise == the operator's
compromise*. That is a deliberate trust grant, distinct from "needs a secret" (a read-only plugin that
needs one vault token stays **confined** — see `github_repo_sync`).

Two prior findings constrain the design:
- **No root needed.** A cap-only gateway already holds `CAP_SETUID`, which drops to *any* uid including
  the operator's. So the trusted tier is `run_as: <operator>` on the **same** gateway — not a second
  instance, not a root gateway (see #111: root-hybrid rejected).
- **Reachability.** The gateway must read the trusted plugin's code (discovery + fingerprint), but the
  code lives in the operator's `0700` home. A `ductile`-group ACL (`g:ductile:rX` on the plugin dir,
  `g:ductile:x` on the home) grants the gateway *code-read only* — never the creds, and never to the
  confined accounts. This is preferred over opening the home `0711` to the world.

## Decision

Introduce a third account flavour alongside `unconfined` and `confined`: **credentialed**. It is marked
by a `home:` on the account in config. A credentialed account **drops to its uid exactly as a confined
account does** — the privilege gate is identical — but its *runtime* is rooted at the real home, not a
walled state_dir. It is the inverse of the confined (C) contract.

```yaml
accounts:
  default:   { uid: 1001, gid: 1001, state_dir: /var/lib/ductile/accounts/default }  # confined
  trusted:   { uid: 1000, gid: 1000, home: /home/matt }                              # credentialed
```

**A credentialed plugin gets, at spawn:**
- the drop to `uid:gid` (CAP_SETUID/SETGID — never root; same `Validate` invariant as confined);
- `HOME` = the account's real home (the gateway's own `HOME` is dropped, never leaked), so `~/.ssh`,
  `~/.config/gh`, and the git credential helper resolve;
- cwd = the real home;
- operator-granted `plugin_env_passthrough` env (e.g. `SSH_AUTH_SOCK`) preserved untouched;
- **groups-minimal by default** — the drop resets supplementary groups to `[gid]`, so a trusted plugin
  gets the operator's *files* (uid-based) but NOT the operator's *group* powers (docker = root is a
  supplementary group). Docker (or any supplementary group) is an explicit per-plugin opt-in, parked
  until needed. This keeps "trusted = acts as my identity", not "acts as root".

**A credentialed plugin must NOT** be assumed walled: its compromise reaches everything the operator's
uid can. The entry bar is therefore the discipline the name buys — reviewed/authored code, no deps
fetched at spawn, never pipe external/webhook input into a shell or git arg (there is no wall to catch
a mistake in this tier).

### Mechanism (where it lives)
The encoding and guards were shaped by a luminary grill (Hickey/Liskov/Armstrong/Ousterhout,
2026-06-08) before commit — see "Grill outcomes" below for the *why*.
- **Tier as a closed enum, not a bool+tag.** `ResolvedAccount.Mode` is `AccountMode`
  (`ModeUnconfined|ModeConfined|ModeCredentialed`); `AccountConf.Home` selects the credentialed mode
  via the *single* constructor `configuredAccount`. Predicates: `Drops()` (Mode != Unconfined — the
  drop path), `Credentialed()` (Mode == Credentialed — the runtime), `Walled()` (Mode == Confined).
- `Validate` switches on Mode. The privilege gate (uid>0, never root) is identical for both dropping
  tiers; credentialed additionally **asserts at the seam** that Home is absolute and not `/` (the seam
  does not trust how the value was built, not just config load).
- `subprocess_executor` branches the runtime: `Credentialed()` → `withCredentialedHome` (set `HOME`,
  drop the gateway's, leave XDG/passthrough) + cwd = home; `Walled()` → `withAccountRuntimeEnv`
  (state_dir rebase).
- The boot fs-reconcile **verifies-but-does-not-mutate** a credentialed account (`verifyCredentialedHome`):
  the home must exist, be a real directory (not a symlink), and be owned by the account uid — fail closed
  otherwise. The gateway never chmods the operator's home, but it does *look* (a botched home is caught at
  boot, not as a half-run at spawn). The `Lstat` doubles as the `ductile`-group ACL-reachability check
  (an unreadable home fails loudly); an unreadable plugin_root under it also hard-fails discovery at boot.
- Config load **rejects `home:` on the `default`/fallback tier**: the trusted tier must be reached by an
  explicit `run_as` grant, never inherited by silence (an ungranted plugin must never default to running
  as a real user with their creds).
- Every credentialed account is **named loudly at boot** (`WARN … account is CREDENTIALED …`) so the
  trust grant is auditable, never inferred from a field's presence.

## Consequences

- **One gateway, trust-tiered.** confined/untrusted plugins stay walled (vault, /opt code); trusted
  plugins run as the operator from `~/ductile/plugins` (code read via the `ductile` group). Pipelines
  stay on one event router.
- **Secret zero still holds for the confined side.** The gateway-user owns the age key `0600`; confined
  accounts can't read it. A trusted plugin runs as the operator, who may be root-equivalent (docker) —
  so the boot-time, tier-aware *root-sidedoor audit* (kanban #111) warns loudly: a confined account
  with a side-door is a broken wall (strict-mode fail-closed); a trusted account's root-equivalence is
  logged as informed consent.
- **`run_as: matt` for now; per-plugin tier review later** (operator decision 2026-06-08). Plugins that
  only need a scoped secret should migrate back to confined+vault.

## Grill outcomes (luminary panel, 2026-06-08)

The first cut encoded the tier as `Confined bool` + `Home string` with the runtime guards skipped; a
four-lens grill before commit changed it (all "do not ship as-is"):
- **Hickey / Liskov** — a bool+string is a leaky 2-valued encoding of a 3-valued domain; `Confined`
  silently widened from "walled" to "any drop". Liskov found a real second construction site
  (`mostRestrictedAccount`) that dropped `Home`. → adopt the closed `AccountMode` enum + a single
  constructor; the downgrade-is-always-walled is now explicit.
- **Armstrong** — two fail-OPEN holes: (1) the `default`/fallback tier could silently be credentialed →
  every ungranted plugin runs as the operator (escalation); (2) fs-reconcile "skip" verified *nothing*
  about a credentialed home. Plus: `Validate` trusted the builder for `Home`. → ban `home:` on default,
  verify-don't-mutate the home (fail closed), assert `Home` at the seam.
- **Ousterhout** — the out-of-band `ductile`-group ACL was an undefined error (silent absence at boot).
  → the home `Lstat` + discovery's hard-fail on an unreadable plugin_root make it loud; the credentialed
  tier is named at boot rather than inferred.
