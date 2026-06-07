---
id: 107
status: todo
priority: High
blocked_by: []
tags: [vault, privsep, plugins, secrets, v1.0, dev-workstream]
---

# Make first-party plugins vault-native (the secret-holder enforce path)

> **Nav:** [[83-privsep-epic]] · [[103-privsep-thinkpad-phase2-restore-plugins]] · surfaced during the
> 2026-06-07 Phase-2 migration. This is the "A" path — what it actually takes to run secret-holding
> plugins behind the privsep wall.

**Job story:** *When* a confinable plugin needs a secret on the enforced gateway, *I want* it to
receive that secret from the vault over its stdin `secrets` map under a valid principal, *so* it runs
dropped to an account uid with no env/config secret — the ADR end-state.

## The finding (source-verified, ductile-admin 2026-06-07)

Plugins are **not vault-native**, so the Phase-2 "vault carry + secret_ref config" plan does NOT work:
1. **`PluginConf` has no `secret_ref` field** — only relay + webhook endpoints support `secret_ref`
   (`internal/config/types.go`). A vault secret cannot be load-time injected into a plugin config field.
2. The **only** plugin secret path is the **spawn-time stdin `secrets` map**, which requires BOTH:
   (a) the **principal name == the plugin name exactly** — `Compose(job.Plugin)`, no snake→kebab
   normalization; and (b) the **plugin code reads from the `secrets` map** (not `config`/env).
3. Vault **rejects non-kebab principal names**; plugin names like `discord_notify`, `ap_canary` are
   snake_case → can never register as principals as-named.
4. e.g. `discord_notify/run.py:87` reads `config.get("webhook_url")` — the config map, not secrets.

This is why the 2026-06-06 vault migration only moved ductile's own tokens + webhook/relay secrets
(which DO support `secret_ref`) and left plugin API keys on env.

## Verdict — HEAVY, empirically settled (2026-06-07)

We tested the optimistic "light" path live on discord_notify: imported `discord_webhook_url` to the
vault, set config `webhook_url_ref: discord_webhook_url`, locked+attested, ran the `health` command.
BOTH signals say `_ref` is NOT a config-substitution mechanism for plugins:
1. `config check` ERROR — `plugin "discord_notify" requires config key "webhook_url"` (the validator
   treats `webhook_url_ref` as a *different* key; it does not satisfy the required `webhook_url`).
2. runtime — discord_notify `health` → "No webhook_url configured" → job failed. The `_ref` value was
   never substituted into the key the plugin reads.

So vault→plugin delivery is **only** the stdin `secrets` map. This card is HEAVY as written: per
secret-needing plugin = kebab-rename (principal) + a code change to read `secrets[...]` + register/
authorize/import. Not a config tweak. (`_ref` remains valid for relay/webhook endpoints, just not plugins.)

## Work (per secret-needing first-party plugin: discord notifies, github git-sync, ap_canary, fabric…)
1. **Kebab-rename** the plugin so its name is a valid principal (e.g. `discord_notify` → `discord-notify`),
   updating config keys + any `uses:`/pipeline refs.
2. **Read the secret from the stdin `secrets` map** in the plugin code (replace the `config`/env read).
3. **Register the principal** (== plugin name) + **authorize + import** the secret into the vault.
4. Verify the plugin runs enforced (dropped to its account uid) AND composes its secret.

## Acceptance
Each secret-needing confinable plugin runs on the enforced gateway, dropped to an account uid,
receiving its secret from the vault over stdin — no env, no config-literal. (Blocks the full Phase-2
"migrate everything" goal; until then those plugins stay keyless-only or on the unconfined instance.)

## Narrative
- 2026-06-07: Filed when ductile-admin's source diligence showed the staged `secret_ref`-for-plugins
  scheme couldn't work — plugins read config/env, not the vault stdin secrets map, and snake_case names
  can't be principals. This is the real (dev) cost of confining secret-holders; the keyless three
  (web/youtube/healthdata) restore without it. (by @assistant)
- 2026-06-07: REPO-SIDE ENABLER landed (still todo overall) — added `plugins.<name>.vault_principal`
  so a snake_case plugin maps to a kebab vault principal WITHOUT a rename (composePluginSecrets composes
  under the principal; attestation stays on the plugin name). +schema +test. REMAINING (external, on the
  Thinkpad's ~/Projects/ductile-plugins): each secret-holding plugin must READ its secret from the stdin
  secrets map, + register the kebab principal + import/authorize the secret. (by @assistant)

## PROVEN RECIPE (2026-06-07) — first vault-native plugin live

discord_notify proved end-to-end: webhook delivered from the ENCRYPTED VAULT over stdin (no secret
in config/env), running as uid 1001, attested, `requires_vault: true` (fail-closed). The concrete,
reusable recipe per secret-holding plugin:

1. **Plugin code:** read the stdin `secrets` map (the gateway delivers composed secrets under a
   separate `secrets` field, NOT merged into config). e.g. `resolve_webhook_url(config, secrets)` →
   `secrets["discord_webhook_url"] or config.get("webhook_url")` (vault first, config fallback).
2. **Manifest:** move the secret-bearing config key from `required` → optional (`config_keys.required: []`)
   — else the validator demands it in config (defeating vault delivery).
3. **Vault:** register a **kebab** principal == the plugin instance name; grant/import the secret to it.
4. **Config:** `vault_principal: <kebab-principal>` (the #107 field — maps a snake plugin name to the
   kebab principal without renaming) + `requires_vault: true` (fail-closed) + NO secret literal in config.
5. **Deploy:** relocate plugin code → /opt, `config lock` + `plugin lock --all` (re-attest), restart.

Result: secret over stdin ✓, real downstream call ✓, dropped to uid 1001 ✓, fail-closed on misconfig ✓.
Replicating to: ap_canary (salt), github_repo_sync (config.github_token), + the notify clones (share
the now-vault-native discord_notify base). NOTE: discord_notify run.py + manifest changed in the
EXTERNAL ductile-plugins repo — needs its own commit there (ductile-admin checkpointing).
