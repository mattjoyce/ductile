---
id: 55
status: done
priority: High
blocked_by: [50]
tags: [vault, deploy, thinkpad, spawn-hygiene, env, critical]
---

# R5 — Plugin env-passthrough audit (the sleeper risk)

Epic: [[49-epic-thinkpad-vault-field-trial]]. **This is the rung most likely to break the deploy.**
On the branch, plugin children no longer inherit the gateway env — `subprocess_executor` builds
`cmd.Env` from a built-in allowlist + `service.plugin_env_passthrough` only. The Thinkpad's 10
`environment_vars.include` `.env` files are for CONFIG INTERPOLATION and do NOT reach plugins.

So any plugin that today reads an API key from its inherited process env (ANTHROPIC/OPENAI/GEMINI/
YOUTUBE/BRAVE/DISCORD/GITHUB/OLLAMA/CEREBRAS, etc.) will lose it on cutover.

## Steps
1. Per enabled plugin (29 of them), determine how it gets each secret/API key TODAY:
   grep each plugin's `run.sh`/entrypoint + manifest across the three plugin_roots for `os.environ`,
   `$VAR`, `getenv`, etc. Build a table: plugin → env var(s) needed → current source.
2. For each needed var, choose the post-cutover delivery:
   - **plugin_env_passthrough** — add the var name to `service.plugin_env_passthrough` (simplest;
     value still comes from the gateway env via `environment_vars.include`). Use for non-secret/low-risk.
   - **vault** — migrate the secret into the vault and deliver over stdin (preferred for real secrets;
     requires the plugin to read `request.secrets`). Note which plugins already speak the secrets envelope.
3. Produce the concrete `plugin_env_passthrough` list + the vault-migration list as the input to
   [[56-rung6-config-reconciliation]] and [[58-rung8-secret-import-parity]].
4. Confirm the gateway itself still loads the `.env` files (so passthrough values resolve).

## Findings (2026-06-05)
Env names defined across the 10 include files: ANTHROPIC_API_KEY/BASE_URL, OPENAI_API_KEY/BASE_URL,
GEMINI_API_KEY, YOUTUBE_API_KEY, BRAVE_API_KEY, CEREBRAS_API_KEY/BASE_URL, OLLAMA_URL/TIMEOUT,
DISCORD_WEBHOOK_URL, RO_GITHUB_TOKEN, GITHUB_RO_TOKEN, DUCTILE_{LOCAL,TOOL,AGENTICLOOP}_TOKEN, AP_CANARY_SALT.
Built-in allowlist: PATH, HOME, TZ, LANG, LANGUAGE, TMPDIR, LC_* only — no API keys.

- **Safe (config interpolation, delivered via protocol):** DISCORD_WEBHOOK_URL (discord.yaml),
  AP_CANARY_SALT (identity.yaml), DUCTILE_LOCAL_TOKEN (system.yaml).
- **Safe (external CLI with own config):** fabric → reads `~/.config/fabric/.env`, not ductile's env.
  No plugin entrypoint references any LLM API key by name → LLM keys are NOT consumed via plugin env.
- **CONFIRMED breakage:** `claude_harvest/run.py` reads `DUCTILE_LOCAL_TOKEN` directly from env.
- **Soak watch-list (generic os.environ.copy to children):** sys_exec, youtube_transcript,
  astro_rebuild_{prod,staging}, git_commit_push, changelog_microblog, github_repo_sync(getenv).
  Their own secrets come via secret_ref/config; risk is only if a *child process* needs an inherited key.

## Decision (proposed)
Minimal `service.plugin_env_passthrough: [DUCTILE_LOCAL_TOKEN]` to start; validate the watch-list during
soak ([[62-rung12-soak-monitor]]) and add entries reactively. Alternative: broad passthrough of all
API-key names to exactly match today's inherited-env behaviour (safer cutover, weaker hygiene). PENDING Matt.

## Acceptance
- A per-plugin env map exists (plugin → vars → passthrough|vault decision).
- `service.plugin_env_passthrough` candidate list finalised; no enabled plugin is left without a
  documented path to the secrets/env it needs after spawn-hygiene takes effect.
