---
id: 98
status: todo
priority: High
blocked_by: []
tags: [privsep, docs, diataxis, naur]
---

# Privsep · documentation pass (Naur theory + Diátaxis) — take action, not a review

> **Nav:** [[83-privsep-epic]] · last gate before the PR · folds the synthesis **Tier D** doc items
> (`~/Obsidian/Personal1/ductile/privsep/PrivSep-Branch-Review_SYNTHESIS.md`).

**Job story:** *When* a future operator (or AI agent) meets the shipped privsep code, *I want* the
docs to transmit the **theory** behind it and answer task/lookup questions in the right register,
*so* they can run, change, and trust it without reverse-engineering intent from source.

**This is an ACTION card, not a review.** Deliverable = edited docs in the tree, committed. Do **not**
produce another review document. Work against the *settled, folded* code (commits `98c5342`/`3256c23`).

## Grounding

**Naur — *Programming as Theory Building*.** The real asset is the *theory* in the builders' heads:
the world↔program mapping, why it is shaped this way, which alternatives were rejected, what invariants
hold. When that theory isn't written down it dies with the session. So the docs must **capture the
privsep theory**, not just its mechanics: the threat model (own fallible code, not a hostile stranger),
the **privileged-gateway fork** (no uid separation without a CAP_SETUID gateway), the **fail-closed
spine**, the boot-gate truth table, and the three-way vocabulary that must stay distinct —
**account** (privsep OS user) ≠ concurrency **workers** (`service.max_workers`) ≠ vault **principal**.

**Procida — Diátaxis.** Four modes, never mixed: **tutorial** (learning), **how-to** (a task),
**reference** (lookup), **explanation** (understanding/the *why*). Each artifact below declares its mode.

## Actions (each tagged with its Diátaxis mode + acceptance)

- [ ] **[explanation]** ADR (`Ductile - PrivSec and Secrets.md`) is the theory home — verify it still
      reads as the *why* post-fold. **Add T5c:** document that the fingerprint **downgrade path is
      unreachable for vault-principal plugins** (the secret gate fails closed *before* spawn), so the
      `untrusted` tier is, by construction, a home for swapped *non-secret* plugins.
      **Add T17:** the wall is verified **point-in-time at boot**, not continuously — name it as a tradeoff.
      *Accept:* a reader learns the asymmetry + the tradeoff from the ADR, not from the code.
- [ ] **[how-to]** `docs/DEPLOYMENT.md` §5b — "Enable privsep enforce on a host": provision accounts
      (sysusers.d + tmpfiles.d), grant `CAP_SETUID`/`CAP_SETGID`, set the `accounts:` map + `run_as:`
      grants. **T11:** call out the **config ↔ sysusers ↔ tmpfiles uid coupling** as a single
      source-of-truth the operator hand-maintains (boot verifies fail-closed). **T12:** state plainly
      that enforce is **Linux-proven, macOS-pending** ([[95-privsep-launchd-and-live-rollout]]).
      *Accept:* an operator can stand up an enforcing Linux host from the doc alone.
- [ ] **[reference]** A privsep config reference (in DEPLOYMENT.md or schema-adjacent): `accounts:`
      (`uid`/`gid`/`state_dir`), per-plugin `run_as:`, `service.unconfined`; the **boot-gate outcomes
      table** (capability × accounts → enforce/unconfined/refuse); **T14:** `default` and `untrusted`
      are **reserved tier keywords** (one flips ungranted plugins to unconfined-vs-default; the other is
      the downgrade target). Document the new **boot-time refusals/warnings** from the fold: a `run_as`
      grant to an undefined account **fails at config load**; absent `default`/`untrusted` tiers **warn**;
      a fingerprint-mismatch with no `untrusted` tier is **terminal/no-retry**.
      *Accept:* every privsep config key + failure mode is greppable in one place.
- [ ] **[how-to]** **T13:** dev note — running as root with no `accounts:` **refuses to boot**; the
      escape is the loud `service.unconfined: true`. *Accept:* a dev hitting the refusal finds the
      one-line fix in the docs, reads it as designed not a regression.
- [ ] **[reference, tend-not-create]** Confirm the **schema** descriptions + any `ductile … --help`
      privsep surface match the folded behavior; fix drift only. (Tutorial mode: **out of scope** —
      single-operator homelab; note the decision rather than writing one.)

## Acceptance
Docs transmit the privsep theory (Naur) and are sorted into the right Diátaxis register; all Tier D
items (T5c, T11, T12, T13, T14, T17) landed as concrete edits; the three distinct vocab terms stay
distinct; the PR can cite docs that match the code. No review artifact produced — only updated docs.

## Narrative
- 2026-06-07: Created to replace the planned "documentation review" with an action card. The review
  order was code → fold → docs → PR; code review + Tier A+B fold are done (commits `98c5342`/`3256c23`),
  so the doc pass is the last gate before opening the PR. Grounded in Naur (capture the theory) ×
  Procida (Diátaxis registers) at the operator's request. (by @assistant)
