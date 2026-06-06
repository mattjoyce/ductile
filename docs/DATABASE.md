# Ductile: Database Reference

Ductile uses **SQLite 3** for all persistent state, job queuing, and execution history. This document provides the schema definitions and a collection of useful queries for operators.

---

## Database Location
The database is typically named `ductile.db` and resides in your configured `state.path` (default: `~/.config/ductile/ductile.db`).

---

## Schema Overview

### Fact Rows vs Current Rows

Ductile keeps current/cache rows for fast operational reads and append-only
fact rows for durable explanation.

Fact/history rows (durable record):
- `plugin_facts`
- `job_transitions`
- `job_attempts`
- `config_snapshots`
- `event_context`
- `job_log`
- `circuit_breaker_transitions`
- `vault_audit`

Current/cache rows (derived views and operational state):
- `job_queue`
- `plugin_state` (compatibility view of the latest `plugin_facts` row per plugin)
- `circuit_breakers`
- `schedule_entries`

`storage_sequences` is an internal allocator for Ductile-owned fact ordering.
New `plugin_facts` rows receive a monotonic `seq`. Legacy `plugin_facts` rows
may have `seq IS NULL`; those rows keep their old timestamp-only ordering and
should not be treated as perfectly ordered facts.

### 1. `job_queue`
The active work queue. Contains pending, running, and recently completed jobs.

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID | Unique job identifier. |
| `status` | TEXT | `queued`, `running`, `succeeded`, `failed`, `timed_out`, `dead`. |
| `plugin` | TEXT | Name of the plugin or alias. |
| `command` | TEXT | The plugin command (e.g., `poll`, `handle`). |
| `payload` | JSON | Input data for the plugin. |
| `dedupe_key` | TEXT | Used to prevent duplicate enqueues. |
| `event_context_id`| UUID | Reference to the baggage/context for this job. |

### 2. `job_log`
The historical record of completed jobs. Used for auditing and the TUI "Overwatch."

| Column | Type | Description |
|--------|------|-------------|
| `result` | JSON | The full protocol response from the plugin. |
| `stderr` | TEXT | Captured stderr (capped at 64 KB). |
| `last_error` | TEXT | Human-readable error message if the job failed. |

### 3. `event_context`
The "Control Plane" ledger. Stores metadata (Baggage) that propagates through pipelines.

### 4. `plugin_facts`
Append-only durable record of plugin observations. Each row carries a stable
snapshot a plugin emitted as `state_updates`, plus a manifest-declared
`fact_type` and a Ductile-owned monotonic `seq`. **This is the durable plugin
record.** New plugins should declare `fact_outputs` and emit a snapshot from
their durable command.

### 5. `plugin_state`
Compatibility/cache view of the latest fact, one row per plugin. Existing
readers and legacy plugins still on direct write-through see the same
shape they always have. The view is rebuilt automatically by core when a new
fact lands, governed by the manifest's `compatibility_view` (currently
`mirror_object`).

For existing databases, apply required schema migrations before deploy. Startup
validation reports the migration script to run when the database is behind the
runtime schema.

### 6. `schedule_entries`
The persistent state of the scheduler. Tracks when each schedule last fired and when it is due next.

### 7. `circuit_breakers`
Current-state compatibility/cache row for scheduled poll circuit breakers.

### 8. `circuit_breaker_transitions`
Append-only transition facts for circuit breakers. Use this table to explain why a breaker opened, moved half-open, closed, or was manually reset.

### 9. `vault_audit`
Append-only audit facts for the secret vault: every `register` / `set` / `roll` / `revoke` / `purge` and every fail-closed compose `denied`. Columns: `op`, `principal`, `secret_name`, `actor`, `outcome`, `detail`, `created_at`. It records secret **names and outcomes only — never values** (the log is observability, not a second copy of the store). Prefer the CLI inspector `ductile system vault-audit [--principal NAME] [--json]` over raw SQL; the table is here for ad-hoc queries.

---

## Useful Operator Queries

### System Health
```sql
-- Count jobs by status
SELECT status, COUNT(*) 
FROM job_queue 
GROUP BY status;

-- Identify plugins with active circuit breakers
SELECT plugin, command, state, failure_count, opened_at 
FROM circuit_breakers 
WHERE state != 'closed';

-- Show recent breaker transitions
SELECT created_at, plugin, command, from_state, to_state, reason, failure_count, job_id
FROM circuit_breaker_transitions
WHERE plugin = 'my-plugin' AND command = 'poll'
ORDER BY created_at DESC
LIMIT 20;

-- Check for stuck "running" jobs (orphans)
SELECT id, plugin, command, started_at 
FROM job_queue 
WHERE status = 'running' 
  AND started_at < datetime('now', '-1 hour');

-- Vault audit: recent secret-delivery denials for one principal
-- (or use: ductile system vault-audit --principal my-plugin)
SELECT created_at, op, secret_name, actor, outcome, detail
FROM vault_audit
WHERE principal = 'my-plugin'
ORDER BY id DESC
LIMIT 20;
```

### Performance & Troubleshooting
```sql
-- Find the slowest successful jobs in the last 24 hours
SELECT plugin, command, 
       (strftime('%s', completed_at) - strftime('%s', started_at)) as duration_sec
FROM job_log
WHERE status = 'succeeded'
  AND completed_at > datetime('now', '-1 day')
ORDER BY duration_sec DESC
LIMIT 10;

-- Get the latest error for a specific plugin
SELECT completed_at, last_error, stderr
FROM job_log
WHERE plugin = 'my-plugin' AND status = 'failed'
ORDER BY completed_at DESC
LIMIT 1;

-- Inspect recent append-only plugin facts (the durable record)
SELECT seq, created_at, fact_type, job_id, command, fact_json
FROM plugin_facts
WHERE plugin_name = 'file_watch'
ORDER BY
  CASE WHEN seq IS NULL THEN 1 ELSE 0 END ASC,
  seq DESC,
  created_at DESC
LIMIT 20;

-- Inspect a plugin's compatibility view (latest fact, mirrored)
SELECT state FROM plugin_state WHERE plugin_name = 'my-plugin';
```

### Scheduler Inspection
```sql
-- See upcoming scheduled runs
SELECT plugin, schedule_id, next_run_at, last_success_at
FROM schedule_entries
WHERE status = 'active'
ORDER BY next_run_at ASC;
```

---

## Maintenance

### Manual Cleanup
Ductile automatically prunes `job_log` after 30 days, but you can manually vacuum or prune if needed:
```bash
# Prune logs older than 7 days
sqlite3 ductile.db "DELETE FROM job_log WHERE completed_at < datetime('now', '-7 days');"

# Reclaim disk space
sqlite3 ductile.db "VACUUM;"
```

### Performance Tuning
Ductile enables **WAL mode** and **Synchronous=NORMAL** by default for optimal performance on SSDs. You can verify this via:
```sql
PRAGMA journal_mode;
PRAGMA synchronous;
```
