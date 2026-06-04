---
id: 39
status: todo
priority: Normal
tags: [vault, adr, governance, branch-review, merge-readiness]
---

# Vault · ADR acceptance gate + record sole-writer rationale before merge

**From the 2026-06-04 branch reviews (Brooks-Beck §2.2/§3.3).** Two ADR-hygiene items the
reviewers want closed before this branch merges:

- **Keyed-nonce attestation must not land until its ADR amendment is *accepted*, not just
  proposed.** Rung 4 (#12, shipped) couples all plugin verification to the vault per the
  Attestation ADR §4. Confirm the `Ductile - Plugin Attestation and Keyed Fingerprints` ADR
  amendment is marked **accepted** (status field), or mark it so, so the landed behaviour
  reads as a designed decision rather than uncommitted scope creep.
- **Record the single-channel resident-model rationale in the ADR.** Daemon-as-sole-writer
  (not CLI writes) was a conscious choice to avoid split-brain (stale daemon model clobbering
  on `Save`). Document that cascade in the vault ADR so the sole-writer API, admin token, and
  genesis (`vault.go` "sole-writer owner" comment) read as intended, not cruft.

**Note (not carded):** Brooks-Beck's headline recommendation was to split the branch into
rung-sized shippable PRs (Rung 1 + delivery, then 2, then 3). That ship has largely sailed for
this branch; captured here as context, not a task.
