---
id: 49
status: done
priority: High
blocked_by: []
tags: [vault, epic, deploy, thinkpad, age-secrets, spawn-hygiene, field-trial]
---

# Field-trial the `feat/age-secrets-and-spawn-hygiene` branch on the Thinkpad (EPIC)

**CLOSED 2026-06-06.** All 15 rungs done ([[50-rung0-thinkpad-deep-recon]]–[[64-rung14-closeout-docs]],
plus [[65-validate-config-on-boot-rejects-split-config]] / [[66-redeploy-thinkpad-after-65-fix]]). The
branch is live on **all three** hosts: Thinkpad (this epic), Mac m1 ([[67-deploy-vault-branch-macm1]]),
and Unraid ([[68-deploy-vault-branch-unraid]]). Branch remains unmerged. Residual `claude_harvest`
DUCTILE_LOCAL_TOKEN passthrough check folds into ongoing soak. Further deploys proceed as more cards
clear.

**Origin (2026-06-05):** rather than merge the vault/age-secrets + spawn-hygiene branch, run it
as a field trial on the Thinkpad reference instance (`matt-ThinkPad-T14s-Gen-1`). Stepwise:
deep recon → safety net → build → gates → migrate → genesis → lock → cutover → test → soak.
Branch stays unmerged; the Thinkpad runs it as the canary.

## Live recon snapshot (2026-06-05, ssh matt@192.168.86.45) — see [[50-rung0-thinkpad-deep-recon]]
- Installed: ductile **v0.783-68c5b08** (2026-05-27), config `~/.config/ductile/`, DB 116 MB, :8081,
  webhooks :8091, `systemctl --user ductile-local`. 48 discovered / 29 enabled plugins.
- **Currently crash-looping** — `strict_mode: true` preflight fails on 20+ plugin manifest hash
  mismatches vs `.checksums` (NOT vault-related; pre-existing stale-checksum drift). Recovery hint
  is `ductile config lock`. The rollback baseline is therefore red and must be stabilised first.
- No `vault.age` / age key → clean genesis. tokens.yaml has 6 secrets to import.
- `environment_vars.include` loads 10 `.env` files for CONFIG INTERPOLATION only — they do NOT reach
  plugin children on the branch (spawn-hygiene allowlist + `plugin_env_passthrough`). **Dominant risk.**
- Build **in place on the Thinkpad** at `~/Projects/ductile/` (normal process). `GOTOOLCHAIN=auto` +
  the `go 1.25.0` directive auto-selects the 1.25 toolchain inside the module (host default go is 1.22.2).

## Rungs (dependency-ordered)
0. [[50-rung0-thinkpad-deep-recon]] — deep recon (done live; residual per-plugin env/token drill-down)
1. [[51-rung1-safety-net-rollback-baseline]] — backup + stabilise current to a green rollback point
2. [[52-rung2-build-branch-binary-onhost]] — build in place on the Thinkpad (normal process)
3. [[53-rung3-offline-deploy-gate]] — offline `selfcheck` + `config check` on the new binary
4. [[54-rung4-vault-audit-migration]] — add vault_audit table (observability, not a boot gate)
5. [[55-rung5-plugin-env-passthrough-audit]] — per-plugin env sourcing under spawn-hygiene (CRITICAL)
6. [[56-rung6-config-reconciliation]] — secrets.* keys, strict_mode → admission gates, env passthrough
7. [[57-rung7-age-key-genesis]] — keygen (out-of-band custody) + vault init (capture admin token once)
8. [[58-rung8-secret-import-parity]] — vault import tokens.yaml + prove parity (keep tokens.yaml)
9. [[59-rung9-attestation-lock]] — config lock + plugin lock (also clears the stale-checksum crash)
10. [[60-rung10-cutover]] — stop → swap binary → start → verify boot, attestation, healthz
11. [[61-rung11-functional-secret-delivery-test]] — end-to-end secret-to-plugin + vault-audit
12. [[62-rung12-soak-monitor]] — soak: journal, healthz, latency, real fabric/jina jobs
13. [[63-rung13-rollback-runbook]] — written, tested rollback procedure (precondition of cutover)
14. [[64-rung14-closeout-docs]] — update DEPLOYMENT.md vault steps + acceptance

## DEPLOYED 2026-06-05 ✅
Branch **v0.840-29d7679** live on the Thinkpad (`systemctl --user ductile-local`, :8081). Boot log:
"vault secret delivery enabled (compose-time attestation on)". healthz ok, plugins_loaded 48 (== baseline),
circuits 0. Vault genesis done (age key `~/.config/secrets/ductile/age.key`, admin token in 0600
`~/.config/secrets/ductile/vault-genesis-20260604T225434Z.out`). 6 tokens.yaml secrets imported, parity 6/6.
29 plugins attested. R11 secret-delivery + audit + reserved-refusal proven. R12 soak green.
- **#65 RESOLVED (2026-06-05):** redeployed on **v0.841-27797a8** with ALL FOUR admission gates on
  (`validate_config_on_boot: true` boots clean). See [[65-validate-config-on-boot-rejects-split-config]]
  + [[66-redeploy-thinkpad-after-65-fix]].
- **Watch:** claude_harvest (DUCTILE_LOCAL_TOKEN passthrough) not yet exercised live.
- Remaining: R14 [[64-rung14-closeout-docs]] (DEPLOYMENT.md vault section); ongoing soak.

## Acceptance
- Thinkpad runs the branch binary; boot log shows "compose-time attestation on"; healthz ok;
  `plugins_loaded` == pre-deploy baseline; all previously-working plugins still function (env audit held).
- A vault exists; a test secret is delivered to a plugin over stdin and recorded in `vault-audit`.
- tokens.yaml secrets resolve from the vault with proven parity (tokens.yaml retained; retire = #48).
- A tested rollback path exists. Branch is NOT merged to main.

## Out of scope
- Merging to main; retiring tokens.yaml ([[48-epic-retire-tokens-yaml]]); the Unraid/Mac instances.
