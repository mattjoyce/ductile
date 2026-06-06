---
id: 87
status: backlog
priority: Normal
blocked_by: [86]
tags: [privsep, filesystem, deploy]
---

# Privsep · filesystem permissions (generalize the tracer's one perm)

> **Nav:** [[83-privsep-epic]] · after [[86-privsep-spawn-uid-drop]] · before [[90-privsep-negative-test-suite]]

The tracer (#92) locked one file (the age key). This generalizes: lock the whole secrets
surface and give every worker its own writable space, so uid separation actually bites.

**Scope:**
- **`600` on the secrets surface:** age key (already `0600`-checked at load, `docs/SECRETS.md`
  §11), config bundle, and state DB (`ductile.db`) — gateway-owned, unreadable by worker uids.
  **Verify ownership/mode at boot as a FAIL-CLOSED gate when workers are configured (Armstrong, B3):**
  a *warning* that the age key is group-readable is the difference between a wall and a painted one.
  Warn-and-continue is only acceptable in `unconfined` mode; with workers configured it is fatal.
- **Per-worker `0700` state dir** it **owns** (`workers.<name>.state_dir`, #84), created/chowned
  at boot by the privileged gateway; workers cannot read each other's dir.
- Plugin **code** stays readable/executable (code is not secret).
- **Boot reconciliation has a defined partial-failure policy (Armstrong, B3):** it creates missing
  worker dirs with correct owner/mode and never widens perms — but if chowning dir B fails (EPERM /
  mountpoint / full disk) after dir A succeeded, the gateway must **fail closed**, not run half-confined.
  Reconciliation is a boot gate (all-or-refuse), not best-effort, when workers are configured.

**Acceptance:** a worker gets EACCES on key/config/state DB; each worker reads+writes only its
own `0700` dir; plugin code dirs stay executable; boot reconciles worker dir ownership/mode and
**refuses to start** if any secrets-surface check or worker-dir reconciliation step fails while
workers are configured (warn-and-continue only under `unconfined`).

## Narrative
- **Source:** PrivSec ADR §3 Layer 1b + §5.
- Generalizes #92's single-key perm to the full surface + per-worker dirs.
- Each new perm gets its matching negative test landing **with this card** (see #90 — the
  suite is the aggregate/CI gate, not where the tests are born).
- Secret-zero placement (where the key lives per host) is the deploy cards' call (#88/#89, ADR §10 Q3).
