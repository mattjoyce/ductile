---
id: 68
status: backlog
priority: Normal
blocked_by: [67]
tags: [vault, deploy, unraid, docker, rollout, boundary-node]
---

# Deploy the vault branch to the Unraid (prod boundary node)

Rollout step 3 of 3 — the prod boundary node at `192.168.20.4:8888`. Highest blast radius; do it last,
after Thinkpad + Mac are proven.

## Unraid specifics (docker, NOT a host binary)
- Container `/app/ductile`, config host `/mnt/user/appdata/ductile/config` → `/app/config`,
  DB `/mnt/user/appdata/ductile/data/ductile.db`, `docker compose` at `/mnt/user/appdata/ductile/`.
- **Defer to the canonical Unraid procedure** (vault doc: `Ductile Integration Gateway.md`) — NAS-side
  `git pull` → `docker compose up --build -d` → post-rebuild `config lock` refresh → restart. Do not improvise.
- Migrations run via `docker run ... python3 /scripts/migrate-add-vault-audit-table.py /data/ductile.db`.
- Age key custody: out-of-band on the NAS, excluded from appdata backups.
- **Relay coupling:** this box is the relay peer for `relay-unraid-thinkpad-v1` — verify relay HMAC
  still resolves (from vault) after both ends are on the vault path.

## Acceptance
- Unraid container runs the branch with its own vault; healthz ok; secret delivery + relay to Thinkpad
  verified; rollback (prior image/tag) documented.
