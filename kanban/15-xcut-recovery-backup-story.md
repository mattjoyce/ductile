---
id: 15
status: backlog
priority: Normal
blocked_by: [2]
tags: [vault, ops, recovery, deferred]
---

# Cross-cutting · recovery / backup story

Before the vault holds real secrets, decide how to recover it.

**Scope (defer past Rung 1):**
- Option A: an **off-host backup recipient** — multi-recipient age already supports adding a recipient whose key is held off-host.
- Option B: **re-provision-from-origin** — treat the vault as reconstructible from upstream sources of truth.
- Pick one (or both) before production secrets land.

**Acceptance:** a documented, tested recovery path exists before the vault is relied on.

## Narrative
- **Source:** handoff §"Open micro-decisions" #4 ("Before it holds real secrets (defer past Rung 1)").
- The age key itself is secret-zero (local `0600` file); recovery concerns the blob + key custody.

### Framing (2026-06-02) — DECISION PENDING (operator to pick)
Per operator direction this card is **framed, not decided**. Two failure modes to recover from:
(1) **blob loss/corruption** — the age-encrypted store file is gone/damaged; (2) **key loss** —
the `0600` age identity is gone, so the blob is undecryptable (no key ⇒ no recovery, full stop).

**Option A — off-host age recipient (fast restore).**
- *How:* age supports multiple recipients. Add a second recipient whose private key lives off-host
  (another machine / offline media). Every `Save` already re-encrypts the whole blob to all recipients
  (see `vault.Save` → `keyring.Recipients()`), so the off-host party can decrypt any backup copy.
- *Recovers from:* blob loss (restore a copy) AND key loss (off-host key decrypts).
- *Cost/risk:* a second long-lived key that can decrypt every secret — custody burden, and it must be
  rotated out (re-encrypt) if ever exposed. `writeFileAtomic` keeps no `.bak` precisely to avoid stale
  decryptable copies outliving a recipient roll — an off-host recipient is the deliberate, controlled
  exception to that.
- *Hickey/Armstrong read:* state (the secret) now lives in two custody domains; the message contract is
  "every recipient can read everything." Simple to reason about, but widens the blast radius by one key.

**Option B — re-provision from origin (no blob backup).**
- *How:* treat the vault as a *cache*, not a source of truth. Every secret must have a re-derivable
  origin (a provider console, a `roll` that mints fresh, an upstream secret manager). Recovery = re-`init`
  the vault and re-`set`/`roll` each secret from its origin.
- *Recovers from:* blob loss AND key loss (you rebuild from scratch).
- *Cost/risk:* only works if **every** secret genuinely has a re-derivable origin; a hand-typed/one-off
  secret with no upstream is unrecoverable. Requires discipline (no orphan secrets) + a documented
  inventory of origins. Slower recovery (manual re-provision).
- *Hickey/Armstrong read:* keeps a single custody domain (no second key to guard); pushes the recovery
  guarantee onto a *process* invariant (every secret re-derivable) rather than a stored artifact.

**Both (defence in depth):** off-host recipient for fast routine restore; re-provision as the cold-start
fallback if both blob and all keys are lost. Highest resilience, highest custody + discipline cost.

**Recommendation to weigh:** for a solo homelab, **B** is the YAGNI-aligned floor (no second key to
guard, matches the existing no-`.bak` posture) *provided* every secret has a real origin; add **A** only
once a non-re-derivable secret must live in the vault. **Open question for the operator:** do any planned
secrets lack a re-derivable origin? If yes → A (or Both) is required before prod.
