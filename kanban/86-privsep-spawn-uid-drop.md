---
id: 86
status: done
priority: High
blocked_by: [84, 85]
tags: [privsep, dispatch, spawn]
---

# Privsep · spawn-time uid/gid drop (generalize + make it correct)

> **Nav:** [[83-privsep-epic]] · after [[84-privsep-workers-table]] + [[85-privsep-per-plugin-worker-grant]] · before [[87-privsep-filesystem-permissions]], [[88-privsep-deploy-systemd-launchd]], [[90-privsep-negative-test-suite]]

Generalize the tracer's single drop to every granted plugin — and bake the **correctness of
the drop into the definition of done** (was the separate #91; folded in here because you'd
never ship the fail-open version: a botched drop is *worse than today*, ADR §8).

**Scope:**
- **Don't complect privilege into the lifecycle mutator (Hickey).** Today `configurePluginProcess`
  (`internal/dispatch/process_unix.go:12`) sets `Setpgid` — *how I kill it*. The drop is *who it
  runs as* — a separate concern. Add a **pure builder** `func(ResolvedWorker) *syscall.Credential`
  that the caller composes onto `cmd.SysProcAttr`, rather than reaching for worker state inside a
  void mutator. The credential is then a value the caller can log, test, and assert on.
- Set `cmd.SysProcAttr.Credential = &syscall.Credential{Uid, Gid, Groups: []uint32{gid}}` for the
  `ResolvedWorker` produced by #85, applied in the fork-child window before `execve` (kernel runs
  `setgroups → setgid → setuid`; hence the parent needs the caps).
- **Privileged parent drops; plugin never self-drops.**
- **Definition of done — the drop must be correct (folds in old #91):**
  - supplementary groups **reset** (no inherited gateway groups silently re-granting access);
  - any uid/gid/groups failure **fails the spawn closed** — never fall back to gateway privilege;
  - **the drop failure has its own contract (Armstrong):** a refused drop must surface as a **typed
    error + a `plugin.drop_failed` event**, *distinct* from "binary missing" — today both collapse
    into `subprocess_executor.go:67`'s generic `"start process: %w"`. The drop-failed path must
    **not** feed `retry_policy.go`: retrying a fail-closed privilege drop is pointless churn that
    masks the misconfig.
- **BOOT gate — capability and config must AGREE (Armstrong B2; ADR §5 resolved 2026-06-06):** the
  CAP_SETUID/SETGID probe (and the non-Unix `process_other.go` case) is a *startup* check, not a
  per-spawn discovery. Cross-check *capability held* × *workers configured* and **fail fast at boot
  on any disagreement — no auto-degrade**:
  - capability + workers → enforce the drop;
  - capability + **no** workers → **refuse** (privileged daemon, nothing to drop to = worst case);
  - **no** capability + workers configured → **refuse** (a wall was declared the host can't build);
  - no capability + no workers → `unconfined`, quiet (dev/today).
  The **one** escape hatch is an explicit, loudly-logged **`service.unconfined: true`** — it permits
  `unconfined` despite a configured/privileged host. A silent non-Unix no-op while workers are
  configured is fail-open and must refuse instead. (sshd/nginx/systemd convention: a configured drop
  target that can't be honoured is fatal, never a silent run at gateway privilege.)
- **`unconfined` (ADR §5):** the named no-drop state — spawn as today, gateway uid. Reached only via
  the gate's no-workers+no-capability cell or `service.unconfined: true`. Never named `default`, never
  a synthesised worker (see [[83-privsep-epic]] vocabulary note).
- Linux via `process_unix.go`; `process_other.go` no-op is fine *only* under the boot gate above.

**Acceptance:** every granted plugin spawns under its `ResolvedWorker` uid/gid with supplementary
groups provably reset; a refused drop fails closed with a typed `plugin.drop_failed` (never retried,
never confused with a missing binary); the boot gate refuses on capability/workers disagreement
(both mismatch directions) and on a non-Unix host with workers configured — unless `service.unconfined:
true` is set; the no-workers+no-capability path = today's behaviour byte-for-byte.

## Done (2026-06-06)
- **Boot gate** (`internal/dispatch/bootgate.go` + wired in `cmd/ductile/runtime.go`): pure
  `evaluateBootGate` (capability × workers must agree → refuse on disagreement; `service.unconfined`
  override) — 8 cases tested. Platform probe `hasDropCapability` (root, or Linux CAP_SETUID/SETGID via
  `/proc/self/status`), verified `true` as root on the Dell. Refuse aborts boot; enforce/unconfined/override
  logged. `service.unconfined` config field added.
- **Enforce gate:** `Dispatcher.enforcePrivsep` (`WithPrivsepEnforce`, set from the boot gate). The drop
  is already general (every granted plugin, from the tracer); when not enforcing (dev/override) resolution
  is skipped and plugins run at the gateway uid — today's behaviour byte-for-byte.
- **Drop-failed contract:** `ErrWorkerDropFailed` + a `plugin.drop_failed` event, distinct from a missing
  binary; the dispatcher classifies it **terminal (never retried)**. Verified on macOS (the unprivileged
  EPERM-at-Start path is a typed drop failure) and re-verified on privileged Linux (wall still bites).
- **Groups reset** to the worker's gid; **fail-closed** throughout.
- **Next:** [[87-privsep-filesystem-permissions]] (generalize perms + per-worker dirs),
  [[88-privsep-deploy-systemd-launchd]] (prove the drop under *only* CAP_SETUID, not full root),
  [[93-privsep-fingerprint-bind-grant]], [[90-privsep-negative-test-suite]] (CI aggregate).

## Narrative
- **Source:** PrivSec ADR §3 Layer 1b + §8 (the honest botched-drop risk).
- **Merged (Brooks×Beck review):** old #91 was not an independent increment — a correct,
  fail-closed drop is the *unit*. The optional post-drop self-euid assertion (defence in depth)
  is a later follow-on, noted here, not its own card.
- Generalizes the tracer (#92), which already proved the single-plugin drop end-to-end.
