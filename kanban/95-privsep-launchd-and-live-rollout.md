---
id: 95
status: todo
priority: High
blocked_by: [88]
tags: [privsep, deploy, launchd, ops]
---

# Privsep · launchd (Mac) + live-host rollout (observe-then-next)

> **Nav:** [[83-privsep-epic]] · after [[88-privsep-deploy-systemd-launchd]] (Linux done) · the ADR "one host → observe → next" step

Split out of [[88-privsep-deploy-systemd-launchd]] when its Linux/systemd half shipped + was proven
on real systemd. This card is the **second host + the rollout** — deliberately sequenced after the
Linux host is observed stable (ADR §5: don't light up all hosts at once).

**Scope:**
- **launchd (Mac) equivalent of the systemd unit:** a privileged `LaunchDaemon` that confers the
  drop privilege, worker accounts created at install, the binary never setuid. Mirror `deploy/systemd/`
  shape under `deploy/launchd/`. Update [[docs/MACOS_INSTALLATION.md]].
- **Secret-zero per host (ADR §10 Q3, resolved):** `0600` key file floor now; document the Keychain
  hardening upgrade for the Mac.
- **Live rollout:** deploy the privsep system service onto a real host (Thinkpad #49 first, then Mac m1
  #67), observe a real plugin run for a while, then proceed. Re-deploy after.

**Acceptance:** the Mac host runs the gateway with the drop privilege (not the binary's setuid bit),
drops plugins to workers, worker dirs provisioned by the install; a real first-party plugin observed
running confined on a live host.

## Way forward — de-risk-ordered (2026-06-08)

**Thesis:** the only thing never observed is *"does the setuid wall hold on Darwin?"* Everything else
(plist, `dscl` provisioning, docs) is known-mechanical — the Linux equivalents are done. So prove the
wall FIRST, alone, before any template. The macOS gateway is a **root LaunchDaemon** (launchd has no
cap-only model; an Agent runs as-you and physically can't `setuid`-drop → confinement ⟺ root Daemon).

### Phase 0 — Prove the wall on Darwin (this Mac M1) ← the whole risk — **PROVEN 2026-06-08 ✅**
- **DONE (commit 7b01531):** wall / negative-suite / fsreconcile proofs ported off `_linux` tags →
  `darwin || linux || *bsd` (the `_linux` *filename suffix* alone was forcing linux-only). No `/proc`
  dependency; script-probe based, so they ported unchanged. `capdrop` stays `_linux` (CAP_SETUID-only/
  non-root has no macOS equivalent — the as-root path is the macOS story).
- **`dscl` accounts NOT needed:** `applyAccountCredential` sets a purely numeric `Credential{Uid,Gid,
  Groups:[gid]}` (no `initgroups`), so root setuid-drops to uid 65534/65533 worked with no account
  provisioning. The macOS "setuid to a non-existent uid" wrinkle did not materialize.
- **GREEN on `sudo /tmp/dispatch.test` (root, this M1):** all three PASS —
  `TestReconcileAccountFilesystemAsRoot`, `TestPrivsepNegativeSuite`
  (`key/config/statedb/sibling=DENIED, own=WRITABLE`), `TestPrivsepAccountCannotReadAgeKey` (0600 age
  key → DENIED). **The setuid wall holds on Darwin.** Non-root run SKIPs (skip ≠ pass, #90 honoured).
- **Verdict:** the load-bearing risk is retired — Mac-as-v1.0 is now mechanical (Phases 1–3: templates,
  live deploy-as-new, docs). No SIP/setuid surprise at the mechanism level.

### Phase 1 — Templates — **DONE 2026-06-08 ✅**
- **`deploy/launchd/com.mattjoyce.ductile.plist`** (new): root LaunchDaemon (no `UserName` key →
  runs as root), `RunAtLoad` + `KeepAlive{SuccessfulExit=false}` (= systemd `Restart=on-failure`),
  stdio → `/var/log/ductile/*` (no journal on macOS), binary `/usr/local/bin/ductile`, never setuid.
  `plutil -lint` OK; verified no `UserName` key (runs-as-root) via PlistBuddy.
- **`deploy/install-macos.sh`** (new, NOT a branch in install.sh — kept the live-proven Linux
  installer untouched): the launchd peer. `dscl` creates hidden nologin worker accounts
  `_ductile-default`(1001)/`_ductile-untrusted`(1002) — uids mirror Linux so ONE `accounts:` map
  works on both OSes — with a uid-collision guard. Lays the FHS skeleton (root:wheel) under
  **`/opt/ductile`** (`etc` 0700, `var` 0711 + worker dirs 0700, `plugins` world r-x, `log`), installs
  binary 0755 to `/usr/local/bin`, installs the plist root:wheel 0644. `bash -n` clean; BSD-`install`
  flag form + awk collision logic dry-run-verified on Darwin. **(Originally `/etc`+`/var`; relocated to
  `/opt` during Phase 2 — macOS symlink guard, see Phase 2 below.)**
- **macOS asymmetry captured in the header:** gateway is root → no unprivileged gateway account (only
  workers), and its boot fs-reconcile owns the account dirs itself (the Phase-0 reconcile path); Linux
  needs tmpfiles.d because cap-only can't chown. FHS paths mirror Linux for config parity; only the
  plist lives in the mandatory `/Library/LaunchDaemons/`.
- **NOT yet run live** (that's Phase 2): no `dscl`/`launchctl` executed — templates + installer authored
  and statically validated only.

### Phase 2 — Deploy-as-new on this Mac + observe live — **PROVEN 2026-06-08 ✅ (live MacM1)**
- **Wall holds LIVE under a root LaunchDaemon.** `sudo` deploy-as-new on this M1: install-macos.sh
  (accounts+FHS+binary+plist) + lean config (no API/vault) + `sys_exec` confined `run_as: untrusted`
  + a root `0600` secret. Boot log: `privsep enforcing: plugins drop to their resolved account` →
  job dropped to **uid=1002(_ductile-untrusted)** → `cat /opt/ductile/etc/secret/age.key` →
  **Permission denied**. Real first-party plugin, confined, live host. (Independent check:
  `sudo -u _ductile-untrusted cat <secret>` → denied.)
- **macOS layout = `/opt/ductile` base, NOT `/etc`+`/var`.** Discovered live: `/etc` & `/var` are
  symlinks to `/private/*` on macOS, and ductile's **runtime** refuses symlinked config paths (the
  path-swap guard — stricter than `config check`, which only WARNs). Fix = real paths under `/opt`
  (same rationale as Homebrew's `/usr/local/etc`), NOT weakening the guard with `allow_symlinks`.
  Plist + install-macos.sh + deploy recipe all relocated to `/opt/ductile/{etc,var,log,plugins}`.
- **Two non-blocking findings:** (a) the probe job reads `status: failed` because `cat` of a denied
  file exits 1 — the failure IS the wall; verdict file is truth. (b) `WARN: no default account tier`
  — lean config defined only `untrusted`; a REAL config MUST add a `default` account or ungranted
  plugins run at the gateway uid (= **root** on macOS). → Phase 3 docs.
- Deploy harness was `/tmp/phase2-deploy.sh` (throwaway proof rig); the real how-to is Phase 3.

### Phase 3 — Honest docs + close
- `docs/MACOS_INSTALLATION.md`: root-LaunchDaemon posture + SIP/`task_for_pid` threat-model note (do
  NOT claim Linux-identical). Flip #95 → done, #102 5th gate green → **v1.0**.

**Scope discipline:** Phase 0 proves the **confined wall** (the v1.0 security property). The
credentialed/hybrid tier rides the same `_unix` mechanism already proven on Linux — comes along free;
don't let it bloat the proof. One property, proven on Darwin, is the milestone.

## Narrative
- **Source:** carved from #88 (2026-06-06) when the Linux deliverable was complete + verified; the
  Mac port and the live rollout are ops/observation work, not code, so they ride on their own card.
- The mechanism (#92/#84/#85/#86/#87) and the Linux deploy (#88) are done — this is purely per-host reality.
- **2026-06-08: promoted backlog→todo, Normal→High.** Matt decided the Mac M1 **is** a v1.0 production
  host ([[102-v1.0-readiness-privsep-ship-line]] gate resolved YES), so this is now the last open v1.0
  build/verify item. Confirmed in-code that the mechanism is Darwin-portable (`process_unix.go` uid-drop,
  `fsreconcile_unix.go`, `sidedoor_audit_unix.go` all `_unix`-tagged); the proof harness
  (`privsep_*_linux_test.go`, `fsreconcile_linux_test.go`) is **Linux-build-tagged and must be ported to
  Darwin and run green** before any "wall holds on Mac" claim. macOS enforce = **root LaunchDaemon** (no
  cap-only model) — tradeoff accepted on #102. (by @assistant)
