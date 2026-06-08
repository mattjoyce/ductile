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

## Operator direction (2026-06-07) — FULL conversion, migrate everything
This is no longer "restore a subset" — the new enforced/FHS ductile **becomes the system** and
**everything migrates onto it** (operator: "we are converting to the new ductile... establish it and
migrate everything; you have the shape"). Decisions locked:
- **Scope:** restore ALL currently-enabled confinable integrations.
- **Isolation:** shared `default` uid, accept the §2 sibling-residual (no per-plugin tier yet).
- **fabric:** confine it into the new setup, but **LAST** (home-bound external tool, most work).
- **Unconfinable admin automation → a SECOND, UNCONFINED ductile instance** (admin-role gateway,
  hygiene-only ADR Layer 1a, runs as a privileged user with docker-group/apt access): `docker compose`
  (astro_rebuild), `check-apt-security.sh`, `stopwatch-daily-perf.py`, `file_handler`(/home reader),
  and their `*_notify` siblings. Tracked in [[106-ductile-admin-glue-unconfined-instance]]. The enforced
  gateway is the data plane; this is the ADR data-plane/admin split made concrete (not a fallback).
- **Establish it *properly*, not hand-run** — couple with [[105-v1.0-fhs-install-artifact]] so the
  enforced gateway is laid down from the packaged FHS layout, not the runbook by hand.

## Migration sequence (high level — execute with ductile-admin over radio)
1. Enforced data-plane gateway established via the FHS install (#105) — vault carried, admission on, locked.
2. Migrate all confinable integrations onto it (discord/web/youtube/identity/healthdata/github-confinable),
   repoint paths into account state_dirs, secrets over stdin from vault.
3. Stand up the unconfined admin-glue instance (#106); move docker/apt/perf/file_handler + their notifies there.
4. fabric last — confine onto the enforced side (replicate config into state_dir / move secret to vault).
5. Decommission the old `--user` ductile-local once both new roles are green.

## Acceptance
Confinable real plugins run enforced (dropped to accounts) and do real work; admin automation has an
explicit home; admission gates back on + config/plugin locked; old gateway decommissioned or scoped
to admin-only.

## Narrative
- 2026-06-07: Created at the close of the live Phase-1 enforce cutover (deploy-as-new on the Thinkpad).
  Phase 1 proved the wall with exemplar plugins; this restores real function. The cutover surfaced the
  architectural finding that the gateway mixed confinable data plugins with unconfinable admin
  automation — Phase 2 formalizes the split. (by @assistant)
