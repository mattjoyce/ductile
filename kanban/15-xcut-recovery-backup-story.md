---
id: 15
status: done
priority: Normal
blocked_by: [2]
tags: [vault, ops, recovery, deferred]
---

# Cross-cutting · recovery / backup story

**DECIDED + CLOSED 2026-06-06 (operator call).** Posture: the **age key is custodied out-of-band in
the operator's KeePass**, so key loss is covered there; backup is a **filesystem-safe tar of all
config + the encrypted vault blob** — which is exactly what `ductile system backup` already produces.
This is a variant of Option A (key held off-host) without a second *age recipient*: one key, custodied
in KeePass, paired with an encrypted-blob archive.

Verified the mechanism already implements this posture (no code change needed beyond a stale-help fix):
- `system backup` (`--scope config` and up) includes the encrypted `vault.age` in the archive and
  **deliberately excludes the age key**, recording the blob/key pairing in `BACKUP_MANIFEST.txt`
  (`cmd/ductile/system_backup.go:305-330`). Tested: `system_backup_test.go:296-344` asserts the blob
  is included and the age key is **never** in the manifest or the tar.
- Restore runbook (untar → reinstall key from KeePass at the resolved path → start) is documented in
  `docs/SECRETS.md` §"Backup and restore".
- **Fixed 2026-06-06:** the `system backup --help` NOTE wrongly claimed "vault blob … NOT yet in any
  scope" — contradicting the implemented+tested behaviour. Corrected to state the blob IS included
  from scope `config` up and the key is excluded by design.

So the acceptance ("a documented, tested recovery path exists before the vault is relied on") is met:
`system backup` + the KeePass-held key is the path.

**Pairing UID added 2026-06-06 (operator request).** Each `system backup` now stamps a 5-char
unambiguous-alphanumeric UID: printed on completion, written to `uid.txt` at the archive root, and
recorded as `backup_uid` in `BACKUP_MANIFEST.txt`. The operator saves the UID **with the age key** in
KeePass so an archive can be paired to the key that decrypts it — needed once `rotate-key` has
produced more than one key. `newBackupUID` (`cmd/ductile/system_backup.go`), tested by
`TestBackupUIDWrittenAndPaired`; documented in `docs/SECRETS.md` §"Backup and restore".

**Follow-up (minor, not blocking):** the older `ductile config backup` (`createConfigBackup`,
`config_manage.go:1396`) does **not** include `vault.age` — operators must use `system backup` for a
recoverable vault archive. Worth either folding `config backup` into `system backup` or warning on it;
captured here, not carded.

---
_Original framing retained below for the trail._

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
