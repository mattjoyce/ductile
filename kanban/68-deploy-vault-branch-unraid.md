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

## DEPLOYED 2026-06-05 (collaborative — Unraid admin executed, MacM1.DuctileDeploy spec/support over walkie-talkie)
**Vault deploy: SUCCESS.** Unraid container on **v0.843-04463d8**, "vault secret delivery enabled
(compose-time attestation on)", healthz green, 0 circuits, 15 plugins attested. Genesis on-box (own age
key under /app/secrets via a new `:ro` compose bind; admin token to 0600). 3 tokens.yaml secrets imported
with parity (birda/wol literal, mattjoyce frozen via --resolve-env). plugin_env_passthrough empty
(AP_CANARY_SALT confirmed config-interpolated, not env). Config cleanup: log_level→service, dropped dead
workspace block.
- **3 admission gates live** (verify_integrity_on_boot, fail_on_drift, require_api_auth). `require_api_auth`
  verified safe (boot-time check only; /healthz auth-exempt; webhooks are separate HMAC listener).
- **validate_config_on_boot DEFERRED (Path B).** Critical catch: `config check` (JSON-schema lint) gave a
  FALSE negative on flat timeout/max_attempts; the boot gate uses the Go-struct KnownFields strict-decode
  (strict_decode.go via runtime.go:436) which rejects them → would have crash-looped. FOLLOW-UP: nest/drop
  flat keys (lengthen-only), re-config-lock, flip validate_config_on_boot:true, verify clean boot.

**RELAY RESTORED end-to-end (bonus — never worked from Unraid before today):** the relay-unraid-thinkpad-v1
HMAC secret was dangling/unresolvable on Unraid (relay_enabled:false). Canonical value transferred securely:
encrypted to the genesis age pubkey on the Thinkpad (plaintext never on the hub), decrypted on-box, parity
sha256==22c4b184…c78b5 confirmed both ends. Two non-vault fixes got the smoketest verified: (1) relay-instances.yaml
added to the include list; (2) service.name set to "unraid-prod" (empty peer header → 403 at the Thinkpad ingress).
Verified: Thinkpad ingress "relay request accepted", peer unraid-prod, key_id v1, event b5427132-041a-4923-b3cf-f44b2679c762, 202 Accepted.

**Status:** doing → only the validate_config_on_boot Path-B flip remains to fully match Thinkpad/Mac (4 gates).

## Recon (2026-06-05, from the Mac)
- ductile healthz OK on `192.168.20.4:8888`: **v0.786-215c63f**, uptime ~23h, 45 plugins, 0 circuits,
  container `/app/ductile`, config `/app/config/config.yaml`. Healthy prod boundary node.
- **No SSH access from here** (`root@192.168.20.4` → permission denied: publickey/password). The
  docker-rebuild deploy needs NAS shell access — **blocked on Matt** (provide access, or run it).
- **Must follow the canonical Unraid procedure** (vault doc `Ductile Integration Gateway.md`):
  git pull → `docker compose up --build -d` → post-rebuild `config lock` (+ `plugin lock --all`) → restart.
  Do NOT improvise on prod.
- **Watch-list for this deploy:**
  - Migration via `docker run ... python3 /scripts/migrate-add-vault-audit-table.py /data/ductile.db`.
  - **Relay coupling:** unraid is the `relay-unraid-thinkpad-v1` peer; after both ends are on the vault,
    confirm the relay HMAC still resolves (value must match on both sides).
  - Container has no macOS keychain (the Mac gmail issue won't recur), but check for dangling
    `environment_vars.include` files and any session/credential-bound services.
  - Highest blast radius — do it last, with a tested rollback (prior image tag).

## Acceptance
- Unraid container runs the branch with its own vault; healthz ok; secret delivery + relay to Thinkpad
  verified; rollback (prior image/tag) documented.
