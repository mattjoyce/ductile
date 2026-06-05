---
id: 66
status: done
priority: High
blocked_by: [65]
tags: [vault, deploy, thinkpad, admission, field-trial]
---

# Re-deploy Thinkpad with the #65 fix; re-enable validate_config_on_boot

Epic: [[49-epic-thinkpad-vault-field-trial]]. The Thinkpad currently runs v0.840 with
`validate_config_on_boot: false` as a workaround ([[65-validate-config-on-boot-rejects-split-config]]).
Once #65 is fixed, redeploy and restore the full admission posture.

## Steps
1. Build the #65-fixed binary (on-host `make build` per [[52-rung2-build-branch-binary-onhost]]).
2. Offline gate: `system selfcheck` + `config check`.
3. Flip `service.admission.validate_config_on_boot: true` in config.yaml; `config check` must now pass
   against the REAL split config (no false "ignored key" failures).
4. `config lock`; cutover (stop → swap → start); confirm clean boot with all four admission gates on
   and "compose-time attestation on".

## Acceptance
- Thinkpad runs the #65-fixed binary with `validate_config_on_boot: true` and boots clean on the real
  config. Confirms #65 is genuinely fixed on the canonical deployment before rolling to other hosts.
