# Operator Handbook

This is the portable, agent-facing version of the `ductile` operator skill
(`skills/ductile/`). If you are an AI agent that has been told to operate a
Ductile deployment but cannot load the skill manifest directly, this page
gives you the same substance.

The agent reads it. The human points at it.

---

## Operating frame: the gateway is the supervisor

Ductile is built on Armstrong's supervisor model. The gateway:

- **Isolates** plugins via spawn-per-command — one plugin cannot corrupt
  another.
- **Detects** errors from the outside via exit code, stdout JSON, and
  stderr.
- **Restarts** without intervention via the queue (at-least-once delivery).
- **Hot-upgrades** config via `system reload` without dropping in-flight
  work.

As operator, you do not fight the supervisor; you **use** it.

### Reload over debug-in-place

When a runtime looks wedged, the default move is **reload**, not poke. The
gateway is designed to be restartable; debugging a stuck process while it
holds the PID lock is harder, less informative, and risks corrupting the
SQLite WAL.

```bash
ductile system reload    # SIGHUP, in-process hot swap
# if that does not resolve:
ductile system status    # confirm the new generation is alive
# if still wedged, restart the service supervisor (launchd / systemd /
# docker compose / whatever runs ductile on this host)
```

(For *why* this is the right discipline, see
[`reload_rca.md`](../reload_rca.md) — the reload deadlock RCA is the
canonical example of why hot-swap must be deterministic.)

---

## Runtime context — your deployment

Ductile is not opinionated about how you deploy it. Wherever you run it,
you'll have:

| What | Default | Typically |
|---|---|---|
| Binary | (built by you) | on `$PATH` or at a project-local path |
| Config dir | `~/.config/ductile/` | overridable with `--config <dir>` or `$DUCTILE_CONFIG_DIR` |
| State DB | `<config-dir>/ductile.db` | SQLite, WAL mode |
| API port | `127.0.0.1:8081` | from `service.api_port` in `config.yaml` |
| Service supervisor | none enforced | launchd, systemd, docker compose, supervisord |
| Auth token | none by default | from `tokens.yaml`, surfaced via env var of your choice |

**Action for the operator setting this up.** Build your own runtime-context
table for the gateways you operate — instance name, binary path, config
dir, DB path, port, service supervisor, auth token env var. Keep it next
to your deployment docs, not in this handbook.

---

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
ductile system doctor                     # Startup and runtime health checks
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

---

## Universal flags

| Flag | Purpose |
|---|---|
| `--json` | Machine-readable output (all read commands) |
| `-v, --verbose` | Internal logic, path resolution, baggage merges |
| `--dry-run` | Preview mutations without committing |
| `--config <dir>` | Override config directory |

---

## The `config lock` ritual

Every config or plugin manifest edit goes through:

```bash
ductile config check          # validate
ductile config lock           # authorize new state (updates .checksums)
ductile system reload         # apply without restart
```

**This is the cross-skill ritual.** Plugin authoring hands off here.
Incident response often discovers a forgotten-to-lock root cause. Owning
this ritual is owning the seam between authoring and operating.

### Config integrity (tiered)

| Tier | Files | On mismatch |
|---|---|---|
| High Security | `tokens.yaml`, `webhooks.yaml`, `scopes/*.json` | Hard fail (refuses to start) |
| Operational | `config.yaml`, `plugins/*.yaml`, `pipelines/*.yaml` | Warn & continue |

---

## Entity addressing

Use `<type>:<name>` syntax with `config show/get/set`:

```bash
ductile config show plugin:withings
ductile config show pipeline:video-wisdom
ductile config set plugin:withings.enabled=false
ductile config show plugin:*          # list all plugins
```

---

## Selfcheck — six read-only invariants

1. `config_discovery` — config dir resolves
2. `config_load` — config parses
3. `pid_lock` — PID file matches a running process
4. `db_integrity` — `PRAGMA integrity_check`
5. `db_schema` — required tables/columns/indexes match embedded baseline
6. `queue_terminal_freshness` — no stale terminal-state `job_queue` rows
   past retention

**WAL safety**: when the gateway holds the PID lock, checks 4-6 are
*skipped* with `detail: "skipped: active gateway holds PID lock — quiesce
before selfcheck"`. The skip is correct behaviour, not a bug.

Real-green pattern: run selfcheck **offline** against the new binary
BEFORE installing. Once installed and running, expect "skipped" on 4-6 —
the proof of correctness is that the gateway started at all, because the
schema validator runs at startup and refuses to open the DB on mismatch.

---

## Backup — atomic point-in-time snapshot

```bash
ductile system backup --to <file.tar.gz> [--scope SCOPE] [--config PATH]
```

Scopes (nested ladder; each adds to the previous):

- `db` — DB snapshot only (SQLite `VACUUM INTO`, safe under concurrent
  writers)
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

---

## Migrations & schema

`internal/storage/schema.sql` is embedded in the binary; the schema
validator runs at startup and refuses to open a DB missing any required
table, column, or index. Schema changes ship as Python scripts at
`scripts/migrate-*.py`, idempotent by design, run with the service
quiesced.

Always backup before migration:

```bash
sqlite3 <db> "PRAGMA wal_checkpoint(TRUNCATE);" && cp <db> <backup-path>
```

---

## LLM capability discovery (`system skills`)

Ductile is designed for LLM operation. Get the current live manifest:

```bash
ductile system skills --config <your-config-dir>
# or set DUCTILE_CONFIG_DIR and run: ductile system skills
```

Outputs Markdown listing all plugin commands with endpoints, schemas, and
semantic anchors (`mutates_state`, `idempotent`, `retry_safe`) plus all
configured pipelines. See
[`DUCTILE_SKILLS_SCHEMA_V1.md`](../DUCTILE_SKILLS_SCHEMA_V1.md) for the
contract that output obeys.

---

## Common workflows

### Trigger a pipeline via API

```bash
curl -X POST http://<host>:<port>/pipeline/<name> \
  -H "Authorization: Bearer $DUCTILE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"payload": {"key": "value"}}'
```

### Trigger a plugin directly (bypasses routing)

```bash
curl -X POST http://<host>:<port>/plugin/<name>/poll \
  -H "Authorization: Bearer $DUCTILE_TOKEN" \
  -d '{"payload": {}}'
```

### Inspect a failed job (routine — no incident analysis needed)

```bash
ductile job inspect <job_id> -v --json
```

### Check gateway health

```bash
curl http://<host>:<port>/healthz
```

---

## Architecture summary (operator view)

- **Governance hybrid**: control plane is SQLite `event_context` baggage;
  filesystem state is plugin-managed. The core does not provision per-job
  workspaces.
- **Spawn-per-command**: each plugin invocation is a fresh process
  (polyglot: bash, python, go, any executable).
- **At-least-once**: jobs survive crashes and are recovered on restart.
- **Immutable audit**: `origin_*` baggage keys can never be overwritten by
  plugins.

---

## Job statuses

`queued` → `running` → `succeeded` / `failed` / `timed_out` / `dead`

If you see `dead` or persistent `failed`, treat as an incident: hand off
to root-cause analysis (the `ductile-rca` skill) rather than continuing
routine operation.

---

## When to load other skills

| Companion skill | When to load it |
|---|---|
| `ductile-plugin-developer` | The work requires touching a plugin's code, manifest, or pipeline composition — not just its config. |
| `ductile-rca` | Symptoms are not understood. *Stuck, hanging, tripped, missing, wrong.* Routine `job inspect` for a known-good system does **not** need RCA. |
| `surface-contract` | Docs and code have drifted; you need to audit and re-align them. |

Full ductile incident lifecycle (`ductile-rca` + this handbook +
`ductile-plugin-developer`) is real and worth keeping in mind.

---

## Reference docs

In the same docs site:

- [Architecture](../ARCHITECTURE.md) — the technical deep dive
- [Deployment](../DEPLOYMENT.md) — host-local deployment + backup patterns
- [Operator Guide](../OPERATOR_GUIDE.md) — day-to-day commands with examples
- [Health Check](../HEALTH_CHECK.md) — invariants checked by `selfcheck`
- [SQL Tightening Log](../SQL_TIGHTENING_LOG.md) — schema-change audit trail
- [Reload RCA](../reload_rca.md) — canonical worked example of the
  reload deadlock RCA pattern

In the repo:

- [`AGENTS.md`](https://github.com/mattjoyce/ductile/blob/main/AGENTS.md)
  — the contributor contract; the design grounding behind these commands
- [`CONSTITUTION.md`](https://github.com/mattjoyce/ductile/blob/main/CONSTITUTION.md)
  — the five pillars; this handbook is Pillar 1 (Run)
