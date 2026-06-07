---
id: 111
status: todo
priority: Medium
blocked_by: []
tags: [privsep, deployment, ergonomics, secret-zero, boot-gate]
---

# Root-gateway "halfway" deployment tier + passwordless-sudo fail-closed check

> **Nav:** [[83-privsep-epic]] · [[109-uv-shebang-plugins-under-privsep]] · proposed by operator
> 2026-06-07 — the full cap-only + dedicated-accounts + FHS privsep is the RIGHT thing but a
> homelab turn-off. Offer a sanctioned middle posture that still protects secret zero.

**Job story:** *When* I run ductile in a homelab and don't want to mint dedicated accounts + do a
deploy-as-new, *I want* a simpler posture that still prevents a plugin reading secret zero (the age
key), *so* the per-principal vault model stays real without the full setup tax.

## The tier: gateway as root, plugins drop to a non-root, NON-SUDO user
- Gateway runs as **root** (instead of the cap-only `ductile` user). Root can traverse `/home` →
  plugins can live in `~/ductile/plugins` (root hashes/locks + execs them), and root **self-heals all
  perms** (the fsreconcile privileged path) → the 0711 dance + tmpfiles pre-provisioning + FHS
  migration all evaporate.
- Plugins drop to a dedicated low-priv user (e.g. `ductile`, NO sudo), state under that user's home.

### Why it's legitimate (preserves secret zero)
Secret zero = the age key. The invariant is "a plugin never runs as the identity that can read it."
Root owns the key 0600; a dropped non-root user can't read it on disk or ptrace the root gateway in
memory. So root-gateway + non-root-drop = **the minimum viable secret-zero wall**. Strictly safer than
the naive "run it all as me" (which makes the key matt-owned + plugin=matt → EXPOSED).

### Tradeoffs the operator accepts (write them down — informed accept)
1. **Gateway is root** — a bug in the gateway itself is a root compromise, not a two-caps one. Harden
   the unit (seccomp etc.) but it's root.
2. **One shared drop-user = no inter-plugin isolation** — plugin A can read B's state + ptrace B's
   in-flight secret. Master key stays safe; per-principal isolation between plugins degrades. Use two
   drop-users (trusted/untrusted) to dial back toward full privsep.

## The required check: refuse a drop target with ANY root-equivalent escalation path
Passwordless sudo is only ONE door. **The check must cover every root-equivalent path the drop user
holds**, or it's security theatre:
- **passwordless sudo** (`sudo -n -l -U <user>` → any `NOPASSWD`).
- **`docker` group membership** — `docker run -v /:/host …` = uncontested root, INDEPENDENT of sudo.
  Proven on the Dell 2026-06-08: matt with NO nopasswd sudo read a root-`0600` file via
  `docker run -v /root:/r:ro alpine cat /r/<file>`. The docker socket = root.
- **`lxd`/`incus` group** — same (container → host root). Likely others (`kvm`+libvirt patterns, etc.).
- writable dir in `secure_path`, custom setuid-root binaries (audit found NONE on the ThinkPad — good).

So "the operator's own login user" is doubly unsafe: it usually has BOTH nopasswd sudo AND docker.
**Dropping nopasswd alone buys almost nothing while docker-group stands** (ThinkPad audit).

### Heuristic: WARN LOUDLY IF ROOT SIDE-DOORS (boot-time, tier-aware)
For each resolved drop account, probe its root-escalation paths at boot and emit a loud structured
warning. Tier-aware, because the trust-tier design (proposal 2026-06-08) makes a side-door mean
*opposite* things by tier:
- **confined / untrusted** drop target WITH a side-door → **the privsep wall is a LIE for it.** Loud
  `SECURITY` warn; in **strict mode, fail closed** (the wall it claims doesn't exist).
- **credentialed / trusted** (run_as matt) WITH a side-door → **root-equivalent AS DESIGNED**
  ("acts as me"). Loud **informed-consent** warn so the accepted risk is on the record, but proceed.

Side-doors to detect (best-effort — absence of detection ≠ proof of containment; say so in the warn):
1. passwordless sudo — `sudo -n -l -U <user>` → any `NOPASSWD` (easiest as a root gateway; cap-only
   may not be able to query — degrade to best-effort).
2. `docker` / `lxd` / `incus` group membership (container socket → host root). Cheap + reliable via
   `/etc/group` — the highest-value check.
3. writable dir in the account's PATH / `secure_path` (binary hijack).
4. setuid-root binaries the account can write.
Default behaviour = **warn loudly** (detection is heuristic; a false positive must not brick the
gateway). Fail-closed is a strict-mode opt-in, and ONLY for confined/untrusted tiers.

## VALIDATED on the Dell (x86, Ubuntu 24.04, 2026-06-07) — all PASS
Empirical test (gateway-as-root model, real hardware):
- ✅ dropped user CANNOT read root-owned 0600 secret zero (permission denied)
- ✅ dropped user CAN write its own 0700 state_dir
- ✅ clean (no-sudo) user has no escalation path
- ✅ **side door confirmed real**: `matt` (has `(ALL) NOPASSWD: ALL`) → `sudo -n cat` → read the root 0600 key
- ✅ the check `sudo -n -l -U <user> | grep NOPASSWD` correctly REFUSES `matt`, OKs the no-sudo user
- ✅ the pushed binary (commit 367c028) runs on x86 Linux

## TODO
- [ ] Implement the nopasswd-sudo boot-gate check in Go (alongside `hasDropCapability` /
      `reconcileAccountFilesystem`) — fail closed on a drop target with passwordless sudo. Handle
      sudo-not-installed (= safe) and no-such-user. Scope: drop accounts only (root/gateway exempt —
      root is never a drop target per `Validate`).
- [ ] Deploy recipe + systemd unit variant (`User=root`) + config example for the tier.
- [ ] ADR/runbook section positioning the three postures (all-as-me ✗ / root+user-drop ✓ / full privsep ✓✓)
      with the two tradeoffs as the operator's informed accept.
- [ ] Verify low/no-code claim against `validateAccounts` + the boot gate (does anything hard-code the
      dedicated-account assumption / forbid a login uid as a drop target?).
- [ ] Note: the safe drop user is a dedicated NON-SUDO user, not necessarily the login user.

## E2E VALIDATED with the real binary (Dell, gateway-as-root, 2026-06-08) — PASS
Ran the actual `ductile` binary as a root systemd unit, `uidcheck` plugin granted `run_as: dplug`
(uid 1002, no sudo), real age key as secret zero. Final dispatch (`status: succeeded`), plugin
self-report written to its state_dir (file owned by `dplug:dplug`):
```json
{ "uid": 1002, "whoami": "dplug",
  "cwd": "…/state/dplug", "home": "…/state/dplug", "xdg_cache": "…/state/dplug",
  "secret_zero_read": "DENIED:PermissionError" }
```
- ✅ gateway ran as **root**, auto-logged `privsep enforcing` (root → has drop capability → enforces).
- ✅ plugin **dropped to dplug (uid 1002)**, NOT root.
- ✅ **C runtime contract delivered**: HOME / XDG_CACHE_HOME / cwd all == the account state_dir;
  plugin wrote state there (owned by dplug).
- ✅ **secret zero protected**: dropped plugin got PermissionError reading the root-owned 0600 age key.

### KEY OPERATIONAL FINDING (mirrors #109 exactly) — reachability of plugin code + state_dir
The drop fails closed (`fork/exec … permission denied`) if EITHER the plugin code OR the account
state_dir sits under a dir the dropped uid can't traverse. Hit twice live: both `plugin_roots` and
the `state_dir` were initially under the **0700 config dir** → dropped uid couldn't traverse →
`execve`/`chdir` EACCES. Fix: keep plugin code + state_dir in **account-traversable paths** (world `r-x`
like the FHS `/opt` model, or `0711` parents — the #109 floor).
- **Direct implication for "plugins in ~/ductile/plugins" (operator's idea):** if you drop to a
  DIFFERENT user than the home owner, that user CANNOT traverse the operator's `0700` home → fails.
  So the hybrid "plugins in home" works only if: (a) drop target == home owner, or (b) home is `0711`,
  or (c) plugins live in a shared world-`r-x` path. Document this — it's the #1 footgun of the tier.

## Narrative
- 2026-06-07: Operator proposed the halfway tier to lower the privsep adoption cliff. Core security
  model + the passwordless-sudo side door validated empirically on the Dell; the nopasswd check is the
  one new fail-closed invariant the tier needs. (by @assistant)
- 2026-06-08: Full e2e with the real binary (gateway-as-root → drop to non-sudo user) PASSES — secret
  zero protected, C runtime contract delivered. Surfaced the plugin-code/state_dir reachability footgun
  (same root cause as #109): both must be account-traversable, not under the 0700 config dir or a
  foreign 0700 home. (by @assistant)

## NO-ROOT e2e VALIDATED (Dell, cap-only gateway, 2026-06-08) — PASS. Root hybrid REJECTED as unnecessary.
Proved the gateway needs NO root: ran it as `ductilegw` (uid 996) with `CapEff=0x c0` (= CAP_SETUID(7)
+CAP_SETGID(6) ONLY, not root) via `systemd-run -p AmbientCapabilities="CAP_SETUID CAP_SETGID"`. It
auto-enforced privsep and:
- **confined `uidcheck`→`dplug` (1003):** secret zero `DENIED`, groups `[1004]` minimal, HOME=state_dir (C).
- **trusted `trusted_demo`→`dmatt` (1002):** ran as dmatt, groups `[1003]` minimal (**`in_docker:false`** —
  groups-minimal is FREE, the drop resets to [gid]); **read dmatt's `~/.ssh` cred via ABS path** ("acts
  as me" works); `~`-via-`$HOME` DENIED because HOME=state_dir → **this is the only gap, fixed by the
  credentialed flavour (HOME=/home/matt)**.
- **`ductile`-group code-read PROVEN in the real gateway:** the cap-only gateway discovered + ran the
  trusted plugin from `/home/dmatt/ductile/plugins` via a `g:ductile:rX` ACL — no root, no home-opening.
  Access matrix: gateway reads CODE, NOT creds (`~/.ssh` denied to gateway); confined `dplug` (∉ ductile) denied.

### → DUCTILE DEPLOYMENT SCENARIOS (empirically grounded)
1. **Full privsep (FHS):** cap-only gateway; ALL plugins confined → dedicated accounts, /opt code, vault.
   Untrusted/multi-source/max isolation. PROVEN.
2. **Hybrid trust-tier (cap-only + `ductile` group):** ONE cap-only gateway. confined plugins → walled
   dedicated accounts + vault; **trusted plugins → run-as-matt from `~/ductile/plugins`** (read via the
   `ductile`-group ACL), reach matt's creds, groups-minimal (docker opt-in only). Homelab; the
   recommended shape. Substrate PROVEN; **only remaining build = the credentialed flavour (real HOME)**.
3. ~~Root gateway~~ — REJECTED; cap-only does everything without root (smaller blast radius).
Cross-cutting: secret zero protected in all (gateway-user owns the 0600 key; confined + trusted-as-matt
can't read it unless matt has a root side-door → the #111 warn-loud audit covers that).

### credentialed `ResolvedAccount` flavour — BUILT + GRILLED + PROVEN (MacM1, 2026-06-08)
3-state `AccountMode` enum (`unconfined|confined|credentialed`), single constructor. credentialed =
drop (uid>0, never root — same Validate gate) BUT real HOME + passthrough env + groups-minimal, NOT the
C state_dir rebase. See `docs/adr/credentialed-runtime-contract.md`.
- **Luminary-grilled before commit** (Hickey/Liskov/Armstrong/Ousterhout — all "do not ship as-is").
  Revisions: Mode enum (not bool+Home tag) + single constructor (fixed a real `mostRestrictedAccount`
  Home-drop bug); **ban `home:` on the default/fallback tier** (closed a silent-escalation hole);
  fs-reconcile **verify-don't-mutate** the home (fail closed, also the ACL-reachability check); `Validate`
  asserts Home absolute/non-`/` at the seam; boot WARN names every credentialed account.
- **Proven on the warm Dell cap-only cast (refactored binary):** trusted→`dmatt` runs as uid 1002 with
  HOME=/home/dmatt, reads `~/.ssh` via `$HOME`, groups-minimal (no docker); confined→`dplug` still walled,
  secret zero DENIED; the boot "account is CREDENTIALED" WARN fired live. Full `go test ./...` + e2e green.
- Reachability solved by a **`ductile`-group ACL** (`g:ductile:x` on the home, `g:ductile:rX` on the
  plugin dir) so the cap-only gateway reads trusted CODE from `~/ductile/plugins` without opening /home
  or exposing creds — NO ROOT. Proven on the Dell.

- 2026-06-08: NO-ROOT cap-only e2e PASSES — trust-tier substrate proven without root; root hybrid
  dropped; deployment scenarios crystallized (1 full-privsep / 2 cap-only hybrid). Credentialed flavour
  is the sole remaining gateway build. (by @assistant)
