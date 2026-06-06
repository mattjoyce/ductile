---
id: 88
status: done
priority: Normal
blocked_by: [86]
tags: [privsep, deploy, systemd, launchd]
---

# Privsep · deploy — first real host (systemd, then launchd)

> **Nav:** [[83-privsep-epic]] · after [[86-privsep-spawn-uid-drop]] · before [[89-privsep-deploy-docker-unraid]]

The gateway can only drop privilege if the init system *gives* it the privilege. **One host
first** (Beck): formalize the tracer's host (systemd Linux), observe a real plugin run for a
while, *then* do the Mac. Don't light up all hosts at once.

**Scope:**
- **systemd (Linux dev/test) FIRST:** launch with **`CAP_SETUID`+`CAP_SETGID`** (`AmbientCapabilities=`
  / `CapabilityBoundingSet=`), **not** full root. Document the unit; keep the unprivileged-utility
  path working (`config validate`, `secrets keygen` run as the caller).
- **Provision worker accounts via `sysusers.d` (Q4 resolved; ADR §5):** ship a `sysusers.d` snippet
  that creates the stable worker uids as `nologin` system accounts (the nginx/postgres package
  pattern) — the daemon **never** runs `useradd` at runtime. The `workers` map (#84) references these
  by uid number.
- **launchd (Mac) SECOND, after the Linux host is observed stable;** create the worker accounts at
  install (launchd equivalent of the sysusers.d step).
- **Binary is NEVER setuid (ADR §11):** privilege is **init-conferred**; on-disk binary stays
  root-owned `0755`, never `chmod u+s`.
- Update `docs/DEPLOYMENT.md` + unit/agent templates.

**Acceptance:** the Linux host boots with exactly `CAP_SETUID`+`CAP_SETGID` (not full root),
drops plugins to workers, binary has no setuid bit, utility subcommands still run as the
caller and can't read the `0600` key. Mac follows once Linux is observed stable.

## Done — Linux/systemd (2026-06-06)
- **Templates shipped** (`deploy/systemd/`): `ductile.service` (User=ductile, `AmbientCapabilities=CAP_SETUID
  CAP_SETGID`, `CapabilityBoundingSet=` same — **not** root; binary never setuid), `ductile-workers.sysusers.conf`
  (stable worker accounts), `ductile-workers.tmpfiles.conf` (per-worker `0700` dirs owned by the workers).
- **CAP_CHOWN finding (surfaced here):** a cap-only gateway can't chown worker dirs → they're init-provisioned
  (tmpfiles.d) and the gateway *verifies* fail-closed (#87 `reconcileWorkerDir` refined accordingly).
- **Proven on real systemd (Dell, `systemd-run`):** with **only** the two ambient caps (non-root, uid 1000) a
  plugin drops to the worker uid (`TestPrivsepDropUnderCapabilityOnly` PASS); **without** the caps the confined
  drop fails closed (`TestPrivsepConfinedSpawnFailsClosedWithoutPrivilege` PASS, cap-only test SKIPS — not a false pass).
  This is the claim the full-root container could not show.
- **Docs:** `docs/DEPLOYMENT.md §5b` (install steps, config `workers:` map, boot-gate behaviour, unprivileged-utility note).
- **Deferred to [[95-privsep-launchd-and-live-rollout]]:** the Mac/launchd port + the live-host rollout
  (Thinkpad #49 / Mac m1 #67) — the ADR's "one host → observe → next" step, an ops action, not code.

## Narrative
- **Source:** PrivSec ADR §5 (per-host reality) + §11. Same model as sshd/nginx/CI runners.
- **Sequenced (Brooks×Beck review):** one host → observe → next. Live hosts Thinkpad (#49) /
  Mac m1 (#67); re-deploy after.
- **Q3 resolved (2026-06-06):** secret-zero is a **`0600` root-owned age key file stored outside the
  config/backup bundle** (nginx/sshd TLS-key pattern) on both hosts — this card just sites it and
  documents the path. TPM-seal (Linux, `systemd-creds`) / Keychain (Mac) are *documented* per-host
  hardening upgrades, **not built here** (ADR §8). The `0600` ownership check already lands via #87/#92.
