---
id: 56
status: done
priority: High
blocked_by: [55, 50]
tags: [vault, deploy, thinkpad, config, admission]
---

# R6 — Config reconciliation for the branch

Epic: [[49-epic-thinkpad-vault-field-trial]]. Update `~/.config/ductile/` so the branch boots clean.

## Changes
1. **Vault keys** (in config.yaml, `secrets:` block):
   - `secrets.age_key_file: <path>` — the out-of-band age key from [[57-rung7-age-key-genesis]]
     (or set `DUCTILE_AGE_KEY_FILE`). Mode 0600.
   - `secrets.vault_file: vault.age` — note: this key is struct-defined but currently MISSING from
     config.schema.json (known schema drift), so `config check` won't validate it — it still works at runtime.
2. **strict_mode → admission gates**: replace `service.strict_mode: true` with an explicit
   `service.admission:` block (`verify_integrity_on_boot`, `fail_on_drift`, `validate_config_on_boot`,
   `require_api_auth`). The alias still works, but make the gates explicit and intentional. Decide
   whether `validate_config_on_boot: true` (fail-closed on unknown keys) is wanted on the trial.
3. **plugin_env_passthrough**: apply the finalised list from [[55-rung5-plugin-env-passthrough-audit]]
   under `service.plugin_env_passthrough`.
4. Validate: `ductile config check --config ~/.config/ductile/` is GREEN (warnings noted/accepted).

## Acceptance
- secrets.age_key_file + secrets.vault_file set; strict_mode migrated to explicit admission gates;
  plugin_env_passthrough applied; `config check` passes (the vault_file schema-drift caveat documented).
