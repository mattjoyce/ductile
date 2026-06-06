---
id: 65
status: done
priority: High
blocked_by: []
tags: [vault, config, admission, schema-struct-drift, field-trial-finding, bug]
---

# `admission.validate_config_on_boot` rejects the real split/grafted config (60 ignored keys)

**Found during the Thinkpad field trial ([[49-epic-thinkpad-vault-field-trial]]), 2026-06-05.**

Enabling `service.admission.validate_config_on_boot: true` (the decomplected successor to
`strict_mode`) makes the daemon **refuse to boot** on a real split config, reporting 60 "ignored
config key(s)". The strict decoder appears to validate **every included file against the top-level
`config.Config` struct** rather than each file's appropriate type:

- `config.yaml: line 1: field log_level not found in type config.Config`
- `discord.yaml … field timeout not found in type config.PluginConf` (× many; also `max_attempts`)
- `tokens.yaml: line 1: field tokens not found in type config.Config`

These keys are all legitimate and honoured by the lenient loader (`config check` passes clean) and by
the previously-running v0.783. So either (a) the strict validator decodes grafted/included files
against the wrong struct (likely — note `tokens` and `timeout` are real fields on *other* types), or
(b) `PluginConf`/`Config` genuinely dropped `timeout`/`max_attempts`/`log_level` on this branch
(would be a silent-ignore regression). Needs a code check to disambiguate.

**Workaround applied on the Thinkpad:** `validate_config_on_boot: false` (the other three admission
gates — `verify_integrity_on_boot`, `fail_on_drift`, `require_api_auth` — remain `true`). The daemon
then boots clean. This is the documented escape hatch the error itself suggests.

## To verify / fix
- [ ] Determine whether the strict decoder is pointed at the wrong struct for included/grafted files.
- [ ] Confirm `timeout` / `max_attempts` / `log_level` are still honoured by the v0.840 loader (not silently dropped).
- [ ] Either fix the validator to decode each file against its real type, or document that
      `validate_config_on_boot` is incompatible with the split-config + tokens.yaml graft layout.
- [ ] Related: [[36-config-schema-struct-drift]] (schema/struct reconciliation), [[48-epic-retire-tokens-yaml]]
      (retiring tokens.yaml removes the `tokens` false-positive).

## Resolution (2026-06-05) — DONE
Root cause split three ways; only the first was a validator bug:
1. **Validator bug (fixed in code):** `strict_decode.go` re-decodes every included file against `Config`
   with `KnownFields(true)`, skipping only `pipelines`. `tokens.yaml`'s `tokens:` is a dedicated
   `yaml:"-"` domain (like pipelines) → falsely flagged. Fix: added `"tokens": true` to
   `dedicatedScopeDomains` + invariant comment + `TestStrictDecodeWarningsSkipsTokensScope`.
   Commit `27797a8` (v0.841) on the branch.
2. **Real dead keys (config rot, validator was RIGHT):** top-level `log_level` belongs under `service:`;
   per-plugin flat `timeout`/`max_attempts` were never valid (PluginConf wants `timeouts:`/`retry:`) —
   identical on main, so dead all along. The configured plugin timeouts had **never applied**; plugins
   ran on the 120s/60s defaults. Notably `astro_rebuild_staging` (a 15m docker build) was capped at 120s.
3. **Activation (lengthen-only, per Matt):** activated only the timeouts LONGER than the default —
   8 plugins nested to `timeouts:{poll,handle}` (astro 15m, github_repo_sync 10m, git/etl/harvest 5m,
   changelog 4m, apt 2m). Dropped the rest to defaults; dropped all flat `max_attempts` (retry stays
   default 4 — tightening deferred). Moved `log_level` under `service:`.

Thinkpad redeployed on v0.841 with **all four admission gates on** (`validate_config_on_boot: true`
boots clean). Verified `astro_rebuild_staging` now loads `timeout_seconds: 900`. See [[66-redeploy-thinkpad-after-65-fix]].

**Follow-ups:** (a) Mac/Unraid configs have the same rot — fix in their rollout (#67/#68). (b) Retry
activation (configs want 1–2 vs default 4) is a deliberate tightening for later. (c) Short per-plugin
timeouts (jina 30s, yt-transcript 60s, etc.) were intentionally NOT activated — revisit if desired.
