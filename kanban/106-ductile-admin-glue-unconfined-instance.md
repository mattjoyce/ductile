---
id: 106
status: todo
priority: Normal
blocked_by: []
tags: [deployment, privsep, admin, unconfined, split, v1.0]
---

# Second ductile instance — unconfined admin-glue gateway

> **Nav:** [[83-privsep-epic]] · [[103-privsep-thinkpad-phase2-restore-plugins]] ·
> [docs/adr/filesystem-layout.md](../docs/adr/filesystem-layout.md). The "where do the unconfinable
> plugins live" answer for the full conversion: their own unconfined ductile role.

**Job story:** *When* the new enforced ductile runs the data plane, *I want* a separate **unconfined**
ductile instance for the privileged admin automation that physically cannot run as an account uid,
*so* the whole estate migrates to ductile (operator: "migrate everything") without weakening the
enforced gateway.

## Why a second instance (not on the enforced gateway, not dropped)

These plugins need privilege/access an unprivileged account uid cannot hold, so they can't live behind
the privsep wall — but they're coupled to ductile's scheduling + pipeline + `*_notify` chain, so they
shouldn't leave ductile either. The ADR's data-plane/admin split is the intended end-state: enforced
gateway = data plane; this = admin/glue plane, **hygiene-only (ADR Layer 1a)** by design.

Members (move off the enforced gateway):
- `astro_rebuild_staging` — `docker compose -f ~/admin/docker-compose.yml up` (needs docker group)
- `apt_security_check` — `check-apt-security.sh` (needs apt/root)
- `stopwatch_perf_daily` — `stopwatch-daily-perf.py`
- `file_handler` — `allowed_read_paths=/home/matt` (broad home access)
- their `*_notify` siblings (astro/apt/stopwatch notifies)

## Shape
- A second ductile service running **unconfined** (no `accounts:` map → boot gate = unconfined,
  quiet) as a **privileged user** (matt, or a dedicated `ductile-admin` user in the docker group).
- Its OWN config + state + port (e.g. `/etc/ductile-admin` or a `--user` instance; distinct listen
  addr from the enforced gateway's 8081). NOT under the enforced gateway's `/etc/ductile`.
- Hygiene-only is honest here: it runs trusted first-party admin scripts the operator wrote; the
  threat it doesn't defend (popped admin plugin) is accepted because these need privilege anyway.

## Acceptance
- Admin automation (docker rebuild, apt check, perf) runs on the unconfined instance + fires its
  discord notifies, exactly as before the conversion.
- The enforced data-plane gateway carries NONE of these (verified: no docker/apt/home-reader plugins
  enabled there).
- Both instances coexist; old `--user` ductile-local decommissioned once both are green.

## Narrative
- 2026-06-07: Filed from the Phase-2 clarification. Operator chose full conversion ("migrate
  everything, you have the shape"); the unconfinable admin set gets its own unconfined ductile role
  rather than staying on the legacy gateway or being dropped — the ADR split made concrete. (by @assistant)
