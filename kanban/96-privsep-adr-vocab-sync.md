---
id: 96
status: todo
priority: High
blocked_by: []
tags: [privsep, docs, adr, review-prep]
---

# Privsep · sync ADR + card vocabulary to the worker→account rename

> **Nav:** [[83-privsep-epic]] · **prerequisite for luminary code review** · follows the code rename (commit `5590091`)

The code was renamed **`worker` → `account` / `run_as`** to stop colliding with the dispatch
concurrency *workers* (`service.max_workers`, the worker pool) and the vault *principal* (commit
`5590091`, 2026-06-07). Config keys: `workers:`→`accounts:`, per-plugin `worker:`→`run_as:`. Go:
`WorkerConf`→`AccountConf`, `ResolvedWorker`→`ResolvedAccount`, `resolveWorker`→`resolveAccount`, etc.

**Already synced (in the rename commit):** all Go code, `schemas/config.schema.json`, `deploy/systemd/*`
(files renamed `ductile-accounts.*`), `docs/DEPLOYMENT.md`. **NOT yet synced:** the ADR and the kanban cards.

**Why now (review-prep):** a luminary reviewing the *code* cross-references the ADR to judge "does this
match intent?" If the code says `account` and the ADR says `worker`, the mismatch is noise that derails
the review. Sync the vocabulary **before** the code review so the inputs are consistent.

**Scope:**
- **ADR** (`~/Obsidian/Personal1/ductile/Ductile - PrivSec and Secrets.md`): swap `worker`→`account` and
  the per-plugin grant `worker:`→`run_as:` throughout — §5 ("Worker model" heading + illustrative config
  + sizing rule), §2/§3/§4 mentions, §10 resolved notes, and the **Glossary** (replace/repoint the
  `Worker` entry to `Account`; keep `principal` distinct). **Keep all design reasoning intact — only the
  term changes.** Add a one-line "Renamed worker→account (2026-06-07), see commit 5590091" provenance note.
- **Cards:** the privsep cards (esp. [[84-privsep-workers-table]] "workers table", and `worker` prose in
  85/86/87/90/93) still say worker. Either light-touch update the live ones or add a rename note to
  [[83-privsep-epic]]'s vocabulary block (done cards are historical — a note may suffice; don't churn them).
- Optionally rename card #84's title/filename ("workers-table" → "accounts-table") if worth it.

**Acceptance:** ADR vocabulary matches the shipped code (`account`/`run_as`), `principal` (vault) and
`workers` (concurrency) remain distinct, design reasoning unchanged; epic vocabulary block notes the rename.

## Narrative
- **Source:** the worker→account rename (operator decision 2026-06-07) to resolve the term collision a
  reviewer would flag (Ousterhout/Hickey: different concepts must not share a name).
- Deliberately split from the code rename so the (large, context-heavy) doc-text edit lands on its own,
  and because it's the gate before [[83-privsep-epic]] goes to luminary code review.
