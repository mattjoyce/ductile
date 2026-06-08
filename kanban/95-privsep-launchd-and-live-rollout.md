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

### Phase 0 — Prove the wall on Darwin (this Mac M1) ← the whole risk
- Lift the four proof files off `_linux` build tags and retag `_unix`/`_darwin`, patching Linux-isms
  (no `/proc`, perms/SIP differences): `privsep_wall_linux_test.go`,
  `privsep_negative_suite_linux_test.go`, `privsep_capdrop_linux_test.go`, `fsreconcile_linux_test.go`.
- Create 2–3 throwaway hidden worker accounts via `dscl` (needs sudo — surface explicitly).
- `sudo go test` (root, so it can really setuid-drop). Assert: confined plugin lands on worker uid ·
  cross-account read of age key → `Permission denied` · drop-without-privilege fails **closed**.
- **Gate:** green → v1.0 claim is real, rest is mechanical. Red/surprise (SIP, setuid semantics) → found
  cheaply, before building install tooling on a false floor.

### Phase 1 — Templates (only after Phase 0 green)
- `deploy/launchd/com.mattjoyce.ductile.plist`: root, `RunAtLoad`+`KeepAlive`, mirrors
  `deploy/systemd/ductile.service`; binary stays root-owned `0755`, **never** setuid.
- Darwin branch in `deploy/install.sh`: `dscl`/`sysadminctl` create hidden worker accounts + `0700`
  dirs (the launchd answer to `sysusers.d`+`tmpfiles.d`).

### Phase 2 — Deploy-as-new on this Mac + observe live
- Config/plugins/secrets **out of `/Users/matt` (0700)**, empty DB, confinable plugins only (the
  Thinkpad recipe). Bootstrap the LaunchDaemon, run one real first-party plugin confined, wall-proof
  live: `sys_exec(id)` → worker uid · `sudo -u worker cat key` → denied.

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
