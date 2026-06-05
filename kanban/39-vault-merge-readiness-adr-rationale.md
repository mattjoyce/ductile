---
id: 39
status: done
priority: Normal
tags: [vault, adr, governance, branch-review, merge-readiness]
---

# Vault · ADR acceptance gate + record sole-writer rationale before merge

> DONE (2026-06-06). Both ADR-hygiene gates closed (ADRs live in the Obsidian vault, not the repo).

**From the 2026-06-04 branch reviews (Brooks-Beck §2.2/§3.3).** Two ADR-hygiene items the
reviewers want closed before this branch merges:

- [x] **Keyed-nonce attestation must not land until its ADR amendment is *accepted*, not just
  proposed.** CONFIRMED already accepted: `Ductile - Plugin Attestation and Keyed Fingerprints`
  carries `status: accepted` (frontmatter) + "**Status:** Accepted — implemented 2026-06-03"
  with the amendments recorded inline. No change required.
- [x] **Record the single-channel resident-model rationale in the ADR.** Added to `Ductile - Vault.md`:
  (1) §3.5.1 now has a "**The cascade is by design, not accrued surface**" paragraph spelling out
  resident-model → sole-writer → authenticated write API → admin token → `vault init` genesis; and
  (2) §5 "Alternatives considered" gained a "CLI writes the blob; daemon reloads — **Rejected
  (split-brain)**" row naming the stale-model/clobbering-`Save`/reload-race failure. Also flipped the
  Vault ADR `status: proposed → accepted` (governance close before merge, operator-approved).

**Note (not carded):** Brooks-Beck's headline recommendation was to split the branch into
rung-sized shippable PRs (Rung 1 + delivery, then 2, then 3). That ship has largely sailed for
this branch; captured here as context, not a task.
