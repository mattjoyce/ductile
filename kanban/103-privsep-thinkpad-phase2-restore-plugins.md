---
id: 103
status: todo
priority: High
blocked_by: []
tags: [privsep, thinkpad, deploy, phase2, fix-forward]
---

# Privsep Thinkpad · Phase 2 — restore confinable plugins on the enforced gateway

> **Nav:** [[83-privsep-epic]] · follow-on to the 2026-06-07 live enforce cutover (Phase 1 proven the
> wall with exemplar plugins). The enforced gateway is up on `/etc/ductile` (system service, `ductile`
> uid 984 + CAP_SETUID/SETGID); **real automation is OFFLINE** until this lands. Runbook:
> `docs/runbooks/privsep-thinkpad-enforce.md`.

**Job story:** *When* the Thinkpad runs the enforced (deploy-as-new) gateway, *I want* my confinable
real plugins restored under account uids, *so* the box does real work again behind the privsep wall —
while the unconfinable admin automation finds a separate home.

## What Phase 1 established (don't re-derive)
- Enforced boot green; `sys_exec` dropped to `uid=1002(ductile-untrusted)`, groups reset clean;
  cross-account `state_dir` EACCES confirmed. Config dir `/etc/ductile` is `0700` ductile-owned.
- Phase 1 config is **vaultless** + exemplar plugins only (echo/py-greet/stress/sys_exec).

## Phase 2 work
1. **Carry the vault:** copy `vault.age` + age key into `/etc/ductile` (ductile-owned `0600`); set
   `secrets.age_key_file`. Re-import/confirm the API token + plugin secrets resolve.
2. **Restore CONFINABLE plugins** (run as `default`, repoint paths into account-readable/writable):
   - `discord_*notify`, `web/jina-reader` — network/API, secret over stdin → likely no change.
   - `youtube_*` → `output_dir` into `/var/lib/ductile/accounts/default/...` (was `/mnt/.../summaries`).
   - `identity/ap_canary` → `log_path` into the account state_dir (was `~/.config/ductile/data`).
   - `healthdata/check_db_garmin` → grant uid 1001 read on the garmin DB (or relocate).
   - relocate any plugin **code** still under `/home/matt` into `/opt/ductile` (world r-x, root-owned).
3. **Re-enable admission + lock:** `validate_config_on_boot`, `verify_integrity_on_boot`, then
   `config lock` + `plugin lock --all` against the new layout.
4. **DX:** `ductile job inspect` as `matt` now hits EACCES on `/etc/ductile` (0700) — document reading
   results via the API or as the `ductile` user.

## Decision needed — home for the UNCONFINABLE admin automation
`docker compose` (astro_rebuild_staging), `check-apt-security.sh`, `stopwatch-daily-perf.py`,
`file_handler`(reads `/home/matt`), `fabric` (bound to `~/.config/fabric`) **cannot** run as an
unprivileged account uid. Pick: (a) keep them on the old `--user` gateway (run both gateways), or
(b) a dedicated 2nd unconfined gateway for admin glue, or (c) drop them. Recommend (a)/(b) — privsep
is for the data plane, admin glue stays unconfined by design.

## Acceptance
Confinable real plugins run enforced (dropped to accounts) and do real work; admin automation has an
explicit home; admission gates back on + config/plugin locked; old gateway decommissioned or scoped
to admin-only.

## Narrative
- 2026-06-07: Created at the close of the live Phase-1 enforce cutover (deploy-as-new on the Thinkpad).
  Phase 1 proved the wall with exemplar plugins; this restores real function. The cutover surfaced the
  architectural finding that the gateway mixed confinable data plugins with unconfinable admin
  automation — Phase 2 formalizes the split. (by @assistant)
