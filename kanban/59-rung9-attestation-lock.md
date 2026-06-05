---
id: 59
status: done
priority: High
blocked_by: [56, 57]
tags: [vault, deploy, thinkpad, attestation, config-lock]
---

# R9 — Attestation lock (config lock + plugin lock)

Epic: [[49-epic-thinkpad-vault-field-trial]]. The branch keys plugin fingerprints with the vault nonce
and re-verifies identity at compose time. Attestation only engages once fingerprints are locked AGAINST
the new keyed-nonce scheme — so this must run AFTER genesis ([[57-rung7-age-key-genesis]]) created the nonce.

Bonus: this also clears the pre-existing stale-checksum crash seen in recon (the 20+ manifest mismatches)
by re-blessing every plugin manifest under the new binary.

## Steps
1. With the new binary + vault in place (service can be stopped or the offline path used):
   `ductile config lock --config ~/.config/ductile/` — re-bless config + plugin manifests under the
   keyed-nonce fingerprint scheme. Review the diff first (expected plugin edits, not tampering).
2. `ductile plugin lock <name>` for each plugin that must deliver/receive vault secrets (per the
   secret-delivery design, plugin fingerprints must be recorded for compose-time attestation).
3. Confirm `.checksums` now matches all live manifests (no mismatch remains).

## Acceptance
- `config lock` + `plugin lock` complete under the new binary; no manifest hash mismatch remains;
  the instance is primed for "compose-time attestation on" at boot (verified at [[60-rung10-cutover]]).
