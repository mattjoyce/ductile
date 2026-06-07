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
