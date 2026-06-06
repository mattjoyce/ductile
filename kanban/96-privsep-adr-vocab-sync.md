---
id: 96
status: done
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

## Done (2026-06-07)

**ADR** (`~/Obsidian/Personal1/ductile/Ductile - PrivSec and Secrets.md`) vocabulary synced
`worker`→`account` / `worker:`→`run_as:` throughout — abstract, §2 (contract + accepted-residual
callout), §3 (Layer 1b), §4 (authority chain + table), §5 (heading "Account model", `accounts`
table, vocab + boot-gate callouts, illustrative YAML, provisioning, per-host reality, dev fallback),
§6 (alternatives), §7–§9 (scope/consequences/acceptance), §10 (resolved notes), §11 (schema decision),
and the **Glossary** (`Worker`→`Account` entry repointed; `default`/`unconfined`/boot-gate entries).

- **Illustrative YAML** updated to match the *shipped* schema/deploy: `accounts:` map with
  `state_dir: /var/lib/ductile/accounts/<name>` and per-plugin `run_as:`.
- **Provenance note** added under the Status line + in the Glossary (renamed 2026-06-07, commit `5590091`).
- **Design reasoning unchanged** — terminology-only edits. `principal` (vault) and concurrency
  `service.max_workers` left untouched; tier names `default`/`untrusted` preserved verbatim.
- **Verification:** final ADR grep for `\bworker` leaves only intentional explanatory mentions
  (the rename note + the concurrency-`workers` disambiguation). Independent audit agent confirmed
  zero stray privsep-`worker`, invariants intact.

**Cards:** epic [[83-privsep-epic]] gained a durable rename note in its Vocabulary section (maps all
historical `worker` prose to `account`); done-card prose (84/85/86/87/90/93) left as historical record
per the card's own guidance — no churn. Card #84's filename kept to avoid breaking `[[...]]` backlinks.

## Narrative
- **Source:** the worker→account rename (operator decision 2026-06-07) to resolve the term collision a
  reviewer would flag (Ousterhout/Hickey: different concepts must not share a name).
- Deliberately split from the code rename so the (large, context-heavy) doc-text edit lands on its own,
  and because it's the gate before [[83-privsep-epic]] goes to luminary code review.
- 2026-06-07: ADR + epic vocab synced; #96 closed. Review prereq cleared — implementation is ready for
  the luminary code review (review order: code → docs). (by @assistant)
