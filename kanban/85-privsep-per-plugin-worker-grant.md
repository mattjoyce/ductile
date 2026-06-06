---
id: 85
status: backlog
priority: High
blocked_by: [84]
tags: [privsep, config, authority]
---

# Privsep · per-plugin `worker:` grant (operator-authoritative)

> **Nav:** [[83-privsep-epic]] · after [[84-privsep-workers-table]] · before [[93-privsep-fingerprint-bind-grant]] + [[86-privsep-spawn-uid-drop]]

The *which-worker* decision — and **only** that decision. Fingerprint-binding is split out to
#93 so this card carries one thing: map each plugin to a worker, in the trusted party's config.

**Scope:**
- A per-plugin `worker:` field in the **operator's per-instance plugin config block**
  (`PluginConf`, `internal/config/types.go`) naming a tier from #84.
- **Authority split (ADR §4):** the **manifest is advisory**; the **core config grants**. A
  plugin must never choose its own privilege — ignore any worker/tier hint in the manifest.
- **Default stance (operator decision, Q2):** no grant → the shared **`default`** worker (the
  *configured* unprivileged tier, not `unconfined`; see [[83-privsep-epic]] vocabulary note).
- **Name the value that crosses the resolve→enforce seam (Hickey):** resolution is a **pure
  function** `(PluginConf, WorkersMap) → ResolvedWorker{uid, gid, state_dir, source}` (`source` =
  granted-tier / default / unconfined). This card *produces* that value; #86 *consumes* it; #90
  *asserts on* it. Without a named value the mapping gets re-derived twice and the seam blurs.

**Acceptance:** a plugin runs as the worker its per-instance config names; a manifest worker
hint is ignored; an ungranted plugin resolves to `default`; resolution returns a `ResolvedWorker`
value (no live spawn here — #86 enforces it).

## Narrative
- **Source:** PrivSec ADR §4. CI-runner model: the job declares wants; the admin-configured
  runner decides as-whom it runs.
- **Unbundled (Brooks×Beck review):** fingerprint-binding moved to #93 — different problem
  (supply-chain swap, secondary), so this card stays one decision and ships/observes on its own.
- The grant is *resolved* here; *enforced* (the setuid) by #86.
- Tamper-resistance of this grant file comes from #87's `0600` gateway-owned config (a worker
  can't rewrite its own grant); identity-forgery resistance comes from #93/#12. Two locks.
