---
id: 95
status: backlog
priority: Normal
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

## Narrative
- **Source:** carved from #88 (2026-06-06) when the Linux deliverable was complete + verified; the
  Mac port and the live rollout are ops/observation work, not code, so they ride on their own card.
- The mechanism (#92/#84/#85/#86/#87) and the Linux deploy (#88) are done — this is purely per-host reality.
