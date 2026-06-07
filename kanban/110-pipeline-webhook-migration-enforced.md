---
id: 110
status: todo
priority: Normal
blocked_by: []
tags: [privsep, pipelines, webhooks, notify, vault, enforce, migration]
---

# Pipeline + webhook migration to the enforced gateway (notify chains)

> **Nav:** [[83-privsep-epic]] · [[107-plugins-vault-native-conformance]] · [[103-privsep-thinkpad-phase2-restore-plugins]].
> The deploy-as-new enforced gateway carried NO `pipelines.yaml`; this migrates the chains that light up
> notifications, per-chain, as their plugins come live.

## Model PROVEN end-to-end (2026-06-07) — both pipeline mechanisms under full enforce

- **on-EVENT:** ap_canary (salt from vault) → emits `agent_handshake.registered` → pipeline
  `ap-canary-registered` → `ap_canary_notify` (webhook from vault) → **real Discord post.** LIVE.
- **on-HOOK:** a job fails → `job.failed` lifecycle hook → pipeline `job-failure-notify` →
  `job_failure_notify` (webhook from vault) → **real Discord post.** Infra in place (dormant, see finding 1).
- Every hop: vault-native (secrets over stdin), dropped to uid 1001, attested, `requires_vault` fail-closed.

## Two findings that shape the per-chain work (NOT "trivial carry")

1. **on-hook pipelines are gated by `notify_on_complete: true` on the SOURCE plugin** — they do NOT
   fire on every job; the emitting plugin must opt in (lifecycle hooks ≠ plugin events, per the ADR).
   (So `job-complete-notify` won't spam unless plugins opt in; and a hook notify stays dormant until one does.)
2. **Each notify route needs its OWN `vault_principal`'d instance + principal + grant.** Reusing the
   bare `discord_notify` base via a pipeline/hook delivers NO secret ("No webhook_url") — the principal
   is the instance name. So per chain: a dedicated notify instance (`uses: discord_notify`) +
   `vault_principal: <kebab>` + `requires_vault: true` + grant the webhook secret + carry the route.

## Per-chain recipe
For each chain whose upstream plugin(s) are LIVE on the enforced gateway:
1. notify instance: `<name>_notify: { uses: discord_notify, vault_principal: <kebab>, requires_vault: true }`
2. register the kebab principal + grant `discord_webhook_url` to it
3. carry the pipeline route (`on:`/`on-hook:`) into /etc/ductile/pipelines.yaml; carry the webhook into
   webhooks.yaml if event-driven; `config lock` + `plugin lock --all`; restart
4. fire it and confirm the real post

## Bucket classification (legacy pipelines → where they go)
- 🟢 ENFORCED-carryable (upstream plugins live): `ap-canary-registered` ✅DONE; `job-failure-notify`
  ✅infra (dormant); `github-interest-notify` (IFF `github_interest_switch` is plain-python3 — verify);
  `withings-relay-discord` (needs the relay receiver delivering the event).
- 🟠 ADMIN → [[106-ductile-admin-glue-unconfined-instance]]: `astro-rebuild-staging-on-content-change`,
  `claude-harvest-*`, `apt-security-notify`, `stopwatch-perf-notify`, and `garmin-daily-summary`
  (its step 1 is `run_healthdata_etl` = DOCKER → can't complete on the enforced gateway).
- ⛔ #109 uv → [[109-uv-shebang-plugins-under-privsep]]: `github-repo-sync(-notify)`, `repo-compliance`,
  `repo-changelog`, `repo-maintenance-notify`.
- 🔵 fabric/LLM → [[107-plugins-vault-native-conformance]] (fabric last): `discord-ai`, `youtube-wisdom`,
  `playlist-wisdom`, `web-summarize`, `llm-pipeline`.
- ⚙️ design decision: `job-complete-notify` has no filter → needs a filter/opt-in policy before carrying.

## Acceptance
The enforced-carryable notify chains fire real external posts; each via its own vault_principal'd
instance; pipelines/webhooks for blocked chains wait on #106/#109/#107-fabric. No chain references a
disabled/blocked plugin.

## Narrative
- 2026-06-07: Filed at the Phase-3 wrap. The ap-canary (on-event) + job-failure (on-hook) chains proved
  the full privsep notify model end-to-end; the two findings (notify_on_complete gating; per-route
  vault_principal'd instance) corrected the initial "trivial carry" assumption. Remaining chains are
  per-chain wire-ups as their upstream plugins unblock. (by @assistant)
