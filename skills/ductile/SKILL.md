---
name: ductile
description: >
  Operate, configure, and deploy the ductile integration gateway across its
  three instances. Use when the user wants to: run ductile commands, manage
  the gateway runtime, inspect jobs, trigger API calls, manage tokens or
  webhooks, validate or lock config, deploy to Mac / Thinkpad / Unraid, take
  backups, or run selfcheck. Trigger keywords include "run ductile",
  "config check", "config lock", "system start", "system status", "system
  reload", "job inspect", "plugin list", "deploy ductile", "ductile instance",
  "system backup", "system selfcheck", "Mac ductile", "Thinkpad ductile",
  "Unraid ductile". This skill is the **operator's** seat at the gateway —
  the gateway is the supervisor, and operating it well means trusting that
  supervisor: prefer `system reload` over debugging a wedged runtime in place
  (Armstrong's let-it-crash applied at the operator layer). Pairs with
  `ductile-plugin-developer` (when the change is in plugin code) and
  `ductile-rca` (when the change is an incident, not a routine task).
---

# Ductile — Operator / Admin / Deploy

Ductile is an automation runtime AI agents can run, debug, and build for (see
[`CONSTITUTION.md`](../../CONSTITUTION.md)). This skill is **Pillar 1: Run** —
operating, configuring, and deploying the gateway. Plugin authoring (Pillar
5) lives in `ductile-plugin-developer`; incident analysis (Pillar 3) lives in
`ductile-rca`. Diagnostics (Pillar 2) and plugin testing (Pillar 4) are
planned skills.

## Seam: when to also load

| Companion | When |
|---|---|
| `ductile-plugin-developer` | The work in front of you requires touching a plugin's code, manifest, or pipeline composition — not just its config. |
| `ductile-rca` | Symptoms are not understood. *Stuck, hanging, tripped, missing, wrong.* Routine `job inspect` for a known-good system does **not** need rca. |

Full ductile incident lifecycle (`ductile-rca` + this skill + `ductile-plugin-developer`)
is real and worth keeping in mind.

## Out of scope

- How to write a plugin (→ `ductile-plugin-developer`)
- Plugin manifest / protocol contract (→ `ductile-plugin-developer`)
- Pipeline DSL **authoring** (inspection lives here; composition is plugin-dev)
- Hypothesis design for a failing job (→ `ductile-rca`)

## Operating frame: the gateway is the supervisor

Armstrong's central insight applies directly. The gateway:

- **Isolates** plugins via spawn-per-command — one plugin cannot corrupt another.
- **Detects** errors from the outside via exit code, stdout JSON, and stderr.
- **Restarts** without intervention via the queue (at-least-once delivery).
- **Hot-upgrades** config via `system reload` without dropping in-flight work.

As operator, you do not fight the supervisor; you **use** it.

### Reload over debug-in-place

When a runtime looks wedged, the default move is **reload**, not poke. The
gateway is designed to be restartable; debugging a stuck process while it
holds the PID lock is harder, less informative, and risks corrupting WAL.

```bash
ductile system reload    # SIGHUP, in-process hot swap
# if that does not resolve:
ductile system status    # confirm the new generation is alive
# if still wedged, restart the service supervisor (launchd / systemd / docker compose)
```

(For *why* this is the right discipline, see `docs/reload_rca.md` — the reload
deadlock RCA is the canonical example of why hot-swap must be deterministic.)

## Runtime context — three instances

Lockstep on the same version across all three; ASOF the last known check.
Live state is in `~/.config/ductile/state/`, `/app/state/`, etc.

| Instance | Binary | Config dir | DB | Port | Service |
|---|---|---|---|---|---|
| **Mac (dev)** | `~/.local/bin/ductile` | `~/.config/ductile/` | `~/.config/ductile/ductile.db` | `127.0.0.1:8082` | `launchctl` (`com.mattjoyce.ductile-local`) |
| **Thinkpad** | `~/.local/bin/ductile` | `~/.config/ductile/` | `~/.config/ductile/ductile.db` | `0.0.0.0:8081` | `systemctl --user ductile-local` |
| **Unraid (prod)** | inside container at `/app/ductile` | `/mnt/user/appdata/ductile/config` (host) → `/app/config` (container) | `/mnt/user/appdata/ductile/data/ductile.db` | `192.168.20.4:8888` | `docker compose` at `/mnt/user/appdata/ductile/` |

Auth token shorthand: Mac / Thinkpad use `$DUCTILE_LOCAL_TOKEN`; Unraid dev
token is `Ductilian`. **Vault ops use a different credential** — the vault admin
token (`DUCTILE_VAULT_TOKEN`, minted by `vault init`), not the API token above; the
two are rejected on each other's routes.

## CLI command reference

Pattern: `ductile <noun> <action> [flags]`.

### System
```bash
ductile system start                      # Start gateway (foreground)
ductile system status [--json]            # Health: PID, state DB, plugins
ductile system reload                     # Hot-swap config in a running gateway (SIGHUP)
ductile system watch                      # Real-time TUI monitor
ductile system reset <plugin>             # Reset circuit breaker
ductile system skills [--config <dir>]    # Export LLM skill manifest (Markdown)
ductile system selfcheck [--json]         # Read-only integrity invariants
ductile system backup --to <file.tar.gz>  # Atomic snapshot (VACUUM INTO)
```

### Config
```bash
ductile config check [--json] [--strict]  # Validate syntax, policy, integrity
ductile config lock                       # Authorize state (update .checksums)
ductile config show [entity]              # Show resolved config or entity
ductile config get <path>                 # Dot-notation read
ductile config set <path>=<value>         # Modify (use --dry-run to preview)
ductile config init                       # Initialize config directory
ductile config backup / restore           # Archive / restore configuration
ductile config token / scope              # Manage API tokens and scopes
ductile config plugin / route / webhook   # Manage routing artefacts
```

### Job
```bash
ductile job inspect <job_id> [--json]     # Lineage, baggage, artifacts
ductile job logs [--json]                 # Query stored job logs
  # Filters: --plugin --command --status --submitted-by
  #          --from --to (RFC3339) --query --limit --include-result
```

### Plugin
```bash
ductile plugin list [--api-url URL] [--json]   # Discover loaded plugins
ductile plugin run <name>                      # Manual execution
ductile plugin lock <name>                     # Attest plugin bytes — REQUIRED before it receives vault secrets
```

### Vault & Secrets
The daemon is the vault's **sole writer and key-holder**. Two op classes (model: `docs/SECRETS.md`):
```bash
# Local, key-touching — daemon STOPPED (PID-lock guarded):
ductile vault init   --vault vault.age --key age.key      # genesis: core principal + nonce + one-time admin token
ductile vault import --config <dir> --tokens tokens.yaml  # migrate tokens.yaml into the vault
ductile vault rotate-key --config <dir>                   # rotate the at-rest age key

# Keyless API clients — daemon RUNNING (admin token: --token / DUCTILE_VAULT_TOKEN):
ductile vault register-principal --name <p> --kind plugin|consumer|gateway
ductile vault set    --name <s> --principal <p>   # value from stdin (--pattern manual|auto)
ductile vault roll|revoke --name <s>              # supersede value | terminal (clears value)
ductile vault revoke-principal|purge-principal|roll-principal --name <p>

ductile secrets keygen|encrypt|rotate            # age-at-rest tooling for config bundles (NOT the vault)
ductile system vault-audit [--principal <p>]     # the append-only audit log (names + outcomes, never values)
```

### API (direct gateway calls)
```bash
ductile api /jobs
ductile api /plugin/echo/poll -f message="hello"
ductile api /pipeline/youtube-wisdom -f url="…"
ductile api /system/reload -X POST
ductile api /healthz
# Flags: -X METHOD, -f key=value, -H Header:val, -b BODY, --api-url, --api-key
```

### Top-level
```bash
ductile skills            # Export capability registry as LLM Markdown
ductile version           # Version + commit + build time
```

## Universal flags

| Flag | Purpose |
|---|---|
| `--json` | Machine-readable output (all read commands) |
| `-v, --verbose` | Internal logic, path resolution, baggage merges |
| `--dry-run` | Preview mutations without committing |
| `--config <dir>` | Override config directory |

## The lock rituals (`config lock` ≠ `plugin lock`)

Locking is **two decoupled acts**, by design. `config lock` re-hashes the config
*files*; `plugin lock` attests a plugin's *bytes* (manifest + entrypoint). A
`config lock` taken for an unrelated edit no longer re-blesses plugin bytes — that
closes "lock-laundering" (a lock for one reason silently approving a swapped
binary). Pick the ritual by what changed:

```bash
# Config file edit (config.yaml / api.yaml / pipelines / webhooks):
ductile config check          # validate
ductile config lock           # authorize new config state (updates .checksums)
ductile system reload         # apply without restart

# Plugin manifest or entrypoint edit:
ductile plugin lock <name>    # re-attest the plugin's bytes (re-records its fingerprint)
ductile system reload
```

A plugin whose bytes changed but was **not** re-`plugin lock`-ed fails its
spawn-time fingerprint check and is **refused its vault secrets** (fail closed) —
a routine `config lock` will not fix this. Plugin authors
(`ductile-plugin-developer`) hand off at `plugin lock`; incident responders
(`ductile-rca`) see "forgot to `plugin lock` after a manifest change" as a
recurring root cause.

### Config integrity (tiered)

| Tier | Files | On mismatch |
|---|---|---|
| High Security | `tokens.yaml`, `webhooks.yaml`, `scopes/*.json` | Hard fail (refuses to start) |
| Operational | `config.yaml`, `plugins/*.yaml`, `pipelines/*.yaml` | Warn & continue |

## Entity addressing

Use `<type>:<name>` syntax with `config show/get/set`:

```bash
ductile config show plugin:withings
ductile config show pipeline:video-wisdom
ductile config set plugin:withings.enabled=false
ductile config show plugin:*          # list all plugins
```

## Selfcheck — six read-only invariants

1. `config_discovery` — config dir resolves
2. `config_load` — config parses
3. `pid_lock` — PID file matches a running process
4. `db_integrity` — `PRAGMA integrity_check`
5. `db_schema` — required tables/columns/indexes match embedded baseline
6. `queue_terminal_freshness` — no stale terminal-state `job_queue` rows past retention

**WAL safety**: when the gateway holds the PID lock, checks 4-6 are *skipped*
with `detail: "skipped: active gateway holds PID lock — quiesce before
selfcheck"`. The skip is correct behaviour, not a bug.

Real-green pattern: run selfcheck **offline** against the new binary BEFORE
installing. Once installed and running, expect "skipped" on 4-6 — the proof
of correctness is that the gateway started at all, because the schema
validator runs at startup and refuses to open the DB on mismatch.

In the Unraid container, discovery defaults do not include `/app/config`;
pass `--config /app/config` explicitly when using `docker exec`.

## Backup — atomic point-in-time snapshot

```bash
ductile system backup --to <file.tar.gz> [--scope SCOPE] [--config PATH]
```

Scopes (nested ladder; each adds to the previous):

- `db` — DB snapshot only (SQLite `VACUUM INTO`, safe under concurrent writers)
- `config` (default) — `db` + ductile config dir
- `plugins` — `config` + every dir under `plugin_roots`
- `all` — `plugins` + every file under `environment_vars.include`

Each archive embeds `BACKUP_MANIFEST.txt` with version, commit, hostname,
source paths, SHA256 of source DB, included/excluded items + reasons.
Refuses to overwrite an existing `--to` destination.

Inspect a manifest without re-extracting:
```bash
tar -xzOf <archive>.tar.gz BACKUP_MANIFEST.txt
```

## Migrations & schema

`internal/storage/schema.sql` is embedded in the binary; the schema validator
runs at startup and refuses to open a DB missing any required table, column,
or index. Schema changes ship as Python scripts at `scripts/migrate-*.py`,
idempotent by design, run with the service quiesced.

**Container migrations on Unraid** can run against the existing image's
python3 without installing python on the host:

```bash
docker run --rm \
  -v /mnt/user/appdata/ductile/data:/data \
  -v /mnt/user/Projects/ductile/scripts:/scripts:ro \
  ductile-ductile:latest \
  python3 /scripts/migrate-<name>.py /data/ductile.db
```

Always backup before migration:
```bash
sqlite3 <db> "PRAGMA wal_checkpoint(TRUNCATE);" && cp <db> <backup-path>
```

## LLM capability discovery (`system skills`)

Ductile is designed for LLM operation. Get the current live manifest:

```bash
ductile system skills --config ~/.config/ductile/
# or set DUCTILE_CONFIG_DIR and run: ductile system skills
```

Outputs Markdown listing all plugin commands with endpoints, schemas, and
semantic anchors (`mutates_state`, `idempotent`, `retry_safe`) plus all
configured pipelines. See `docs/DUCTILE_SKILLS_SCHEMA_V1.md` for the
contract that output obeys.

## Common workflows

### Trigger a pipeline via API
```bash
curl -X POST http://localhost:8081/pipeline/<name> \
  -H "Authorization: Bearer $DUCTILE_LOCAL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"payload": {"key": "value"}}'
```

### Trigger a plugin directly (bypasses routing)
```bash
curl -X POST http://localhost:8081/plugin/<name>/poll \
  -H "Authorization: Bearer $DUCTILE_LOCAL_TOKEN" \
  -d '{"payload": {}}'
```

### Inspect a failed job (routine — no rca needed)
```bash
ductile job inspect <job_id> -v --json
```

### Check gateway health
```bash
curl http://localhost:8081/healthz
```

## Architecture summary (operator view)

- **Governance hybrid**: control plane is SQLite `event_context` baggage;
  filesystem state is plugin-managed. The core does not provision per-job
  workspaces.
- **Spawn-per-command**: each plugin invocation is a fresh process
  (polyglot: bash, python, go, any executable).
- **At-least-once**: jobs survive crashes and are recovered on restart.
- **Immutable audit**: `origin_*` baggage keys can never be overwritten by
  plugins.

## Job statuses

`queued` → `running` → `succeeded` / `failed` / `timed_out` / `dead`

If you see `dead` or persistent `failed`, **stop and load `ductile-rca`** —
that is the incident handoff.

## Reference files

In-skill references:
- `references/config.md` — config structure & grafting
- `references/api.md` — REST API endpoints (incl. the `/vault/*` management routes)
- `docs/SECRETS.md` (in-repo) — the canonical secrets + vault + attestation model
- `references/pipelines.md` — pipeline DSL & orchestration (for **operator-level
  inspection and triggering**, not authoring — authoring lives in
  `ductile-plugin-developer`)

In-repo (`~/Projects/ductile/docs/`):
- `DEPLOYMENT.md` — host-local deployment + §10 Backups (systemd-timer cron
  pattern for `system backup`)
- `OPERATOR_GUIDE.md` — day-to-day operator commands incl. backup + selfcheck
- `HEALTH_CHECK.md` — invariants checked by `selfcheck`
- `SQL_TIGHTENING_LOG.md` — schema-change audit trail; per-index re-add
  triggers for Wave 2.E drops

Per-instance canonical procedures (read these for deploy work):
- **Mac**: `MEMORY/WORK/20260503-212500_ductile-sql-tightening-plan/PRD.md`
  iteration 5 — backup → migration → build → codesign → bootout/bootstrap
- **Unraid**: `/Volumes/Projects/unraid_admin/vault/Ductile Integration
  Gateway.md` — NAS-side `git pull` → `docker compose up --build -d` →
  post-rebuild `config lock` refresh → restart. **Do not improvise.**

Cross-instance memory:
- `feedback_unraid_docker_exec` warning is dolt-specific (not SQLite).
  `docker exec ductile sqlite3 …` is safe.
- `feedback_ductile_macos_deploy` — re-codesign at destination after `cp`
  into `~/.local/bin/`.
- `feedback_ductile_redeploy_tcc_reset` — adhoc cdhash changes per rebuild
  reset macOS TCC grants; the binary's `tcc_paths` cold-start prewarm
  handles this without operator intervention.
