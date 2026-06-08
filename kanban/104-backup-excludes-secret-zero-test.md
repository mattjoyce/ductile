---
id: 104
status: done
priority: High
blocked_by: []
tags: [backup, security, secrets, test, adr, invariant]
---

# Test invariant — `system backup --scope config` must NEVER contain secret-zero

> **Nav:** [[83-privsep-epic]] · enforces the load-bearing invariant in
> [docs/adr/filesystem-layout.md](../docs/adr/filesystem-layout.md) (age-key placement decision).

**Job story:** *When* `ductile system backup --scope config` builds a portable archive, *I want* a
regression test that proves the **age key** (and anything under `/etc/ductile/secret/`) is absent,
*so* the sshd-style co-location of secret-zero inside the config tree is safe **by enforcement, not
by a comment** — preserving age-at-rest's whole value (a leaked bundle carries no key).

## Why this exists

The filesystem-layout ADR places the age key at `/etc/ductile/secret/age.key` (0600, sshd pattern)
**on the explicit condition** that the backup-exclusion is a *tested* invariant. Today the exclusion
is asserted in DEPLOYMENT.md §10 ("the age key is excluded") but not enforced by a test — a glob
nobody re-checks is exactly the kind of control that silently rots and one day ships secret-zero
inside every backup tarball. PrivSec §11 principle: a control's integrity must live with its enforcer.

## Acceptance
- A test creates a config with an age key under the config dir, runs `system backup --scope config`,
  and **fails** if the archive contains the age key path or any `secret/` entry.
- Covers the `config`, `plugins`, and `all` scopes (the nested ladder — each must still exclude the key).
- Also assert the `BACKUP_MANIFEST.txt` records the key as excluded (the existing claim becomes checked).
- If the test cannot be made to hold, the ADR's fallback applies: move the key to a sibling outside the
  archived tree (systemd `LoadCredential` / `/etc/credstore.encrypted`) and document that instead.

## Narrative
- 2026-06-07: Filed from the filesystem-layout ADR. The age-key location (sshd-style co-located vs.
  outside the bundle) was debated; co-location was accepted *only* with this test as the guard, so the
  card is the other half of that decision. (by @assistant)
- 2026-06-07: DONE — TestBackupNeverContainsSecretZeroADRLayout locks the invariant under the ADR
  secret/ layout (config + all scopes); complements the pre-existing TestBackupIncludesVaultBlobNotKey.
  Config scope is an explicit allow-list (no age.key), so exclusion is structural + now enforced. (by @assistant)
