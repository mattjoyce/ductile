---
id: 1
status: todo
priority: High
tags: [vault, epic]
---

# Vault — age-backed, ductile-owned secret store (EPIC)

Build a ductile-owned secret store: a single whole-store **age**-encrypted blob held
in memory, with registered **principals**, `authorized_principals` grants, a lifecycle
(register / retrieve / dump / roll / revoke / check), and `secret_ref:` resolution —
replacing today's `${ENV}` delegation.

**Build ladder (cards):**
- Rung 1 (START HERE): #2 store · #3 principals · #4 secret model + set/get/check · #5 Compose · #6 `vault init` · #7 tests
- Architecture: #8 daemon sole-writer + authenticated mgmt API
- Rung 2: #9 migration & `secret_ref:` references
- Rung 3: #10 lifecycle (roll/revoke/purge) · #11 `vault_audit` fact table
- Rung 4: #12 attestation upgrade (with privsep)
- Rung 5: #13 `vaultd` daemon (maybe never)
- Cross-cutting: #14 dispatch delivery wiring · #15 recovery/backup · #16 commit prerequisites

## Narrative
- **Source:** `/tmp/claude-501/HANDOFF-ductile-vault.md`.
- **Authoritative design:** `~/Obsidian/Personal1/ductile/Ductile - Vault.md` (ADR, review-hardened — §3 model/lifecycle, §8 decision log, Glossary = ubiquitous language). Prereq: `Ductile - PrivSec and Secrets.md`. Later rung depends on `Ductile - Plugin Attestation and Keyed Fingerprints.md`.
- Design is **settled and review-hardened** (four adversarial reviews folded into §8); no vault code exists yet.
- Tracking note: the handoff suggested `bd` (beads); per operator direction this work is tracked in **kanban** instead (`/Volumes/Projects` unmounted → fresh local board).
- Constraints: solo/homelab → simplest correct floor, YAGNI. Go. Commit `<component>: <action> <what>`; never attribute AI. TDD encouraged.
