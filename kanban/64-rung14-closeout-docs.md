---
id: 64
status: done
priority: High
blocked_by: [61, 62, 65]
tags: [vault, deploy, thinkpad, docs, closeout]
---

# R14 — Closeout + docs

Epic: [[49-epic-thinkpad-vault-field-trial]]. Capture what the trial taught and make the procedure repeatable.

**Gates the multi-instance rollout** ([[67-deploy-vault-branch-macm1]], [[68-deploy-vault-branch-unraid]]):
the corrected, doc'd path must exist so those hosts don't repeat the Thinkpad crash-loops. Must capture
the two gotchas the trial hit: `plugin lock --all` is separate from `config lock`, and the #65
`validate_config_on_boot` fix.

## Steps
1. Update `docs/DEPLOYMENT.md` with a **vault deploy section**: schema migration (vault_audit),
   `secrets keygen` + `vault init` (admin-token capture + age-key custody), strict_mode → admission
   gates, `plugin_env_passthrough` for spawn-hygiene, attestation lock, and the offline selfcheck gate.
2. Record trial acceptance: boot attestation on, plugins_loaded == baseline, secret-delivery proven,
   tokens.yaml parity green, rollback tested.
3. Capture follow-ups as new cards: e.g. the `vault_file` schema-drift fix, any plugins that needed
   per-spawn vault secrets but didn't speak the envelope, and whether the trial unblocks merge or
   [[48-epic-retire-tokens-yaml]].
4. Note in the epic whether the field trial recommends merging the branch to main.

## Done (2026-06-05)
Structured per Naur × Procida (theory × need / Diátaxis):
- **How-to (need):** `docs/DEPLOYMENT.md` § 11 "Deploying the Vault onto an Instance" — the
  runnable, ordered procedure (backup → vault_audit migration → keygen+genesis → config
  reconcile → import → `config lock` + `plugin lock --all` → cutover → verify), with both
  crash-loop gotchas called out (plugin lock ≠ config lock; validate_config_on_boot surfaces
  dead keys) and a spawn-hygiene pre-cutover check. Ends with a clearly-labelled "Why this
  order (theory)" note — explanation kept separate from the steps, cross-linked to SECRETS.md.
- **Theory (Naur):** `docs/SECRETS.md` already carries the vault mental model; added a
  how-to pointer from § "Genesis and lifecycle" → DEPLOYMENT.md § 11 (explanation → how-to link).
- Committed to the branch (cards intentionally not committed).

Trial acceptance + merge recommendation live on [[49-epic-thinkpad-vault-field-trial]]; follow-ups
filed ([[65-validate-config-on-boot-rejects-split-config]] resolved; [[67-deploy-vault-branch-macm1]],
[[68-deploy-vault-branch-unraid]] queued).

## Acceptance
- DEPLOYMENT.md documents the vault deploy path; trial acceptance recorded; follow-up cards filed;
  a merge/no-merge recommendation captured on the epic.
