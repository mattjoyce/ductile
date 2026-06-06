---
id: 92
status: doing
priority: High
blocked_by: []
tags: [privsep, tracer, dispatch]
---

# Privsep · TRACER — wall off `sys_exec` on one host

> **Nav:** [[83-privsep-epic]] · **root of the build chain** (no blockers) · unblocks [[84-privsep-workers-table]]

The smallest end-to-end slice that delivers *real* isolation and proves the mechanism
composes. Beck tracer-bullet: one worker, one plugin, one host, one negative test. Do this
**first** — before the general worker system — because it walls off the plugin that most
needs it (arbitrary command execution) and tells us whether drop + perms + deploy actually
work together.

**Scope (deliberately thin, deliberately vertical):**
- **One worker, simplest possible config:** a single `untrusted` worker `{uid, gid,
  state_dir}` — minimal shape, no validation/table machinery yet (that's #84).
- **One plugin:** grant only `sys_exec` to it; everything else stays as today.
- **The drop:** `configurePluginProcess` sets `SysProcAttr.Credential` for that one worker,
  **fail-closed** if it can't (no silent run at gateway privilege).
- **One perm that bites:** the age key is gateway-owned `0600`.
- **One host:** the Linux dev/test box (or the Dell test target) running with
  `CAP_SETUID`+`CAP_SETGID`.
- **One negative test:** `sys_exec` run as the worker **cannot read the age key** (EACCES).

**Acceptance:** on the one host, `sys_exec` spawns as the `untrusted` worker uid; the
worker process gets EACCES on the age key; with no worker configured the spawn is unchanged
(dev fallback); the test fails loudly if the wall is breached.

## Narrative
- **Source:** Brooks×Beck review of the privsep epic (2026-06-06). Replaces the all-or-nothing
  bet (#84+#85+#86+#87+#88 before any observable value) with one shippable, observable slice.
- **Real problem first (Brooks):** `sys_exec` is the honest arbitrary-code plugin; isolating
  it is most of the value. The general table (#84) is generality we don't need to prove the wall.
- Everything after this card **generalizes** one layer of this tracer: #84 the config, #85 the
  grant model, #86 the drop, #87 the perms, #88/#89 the deploy, #90 the test suite.
