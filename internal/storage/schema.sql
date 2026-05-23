-- Ductile SQLite Schema
-- This file is embedded into the binary and executed on startup.

-- Job Queue: Current active or pending jobs.
CREATE TABLE IF NOT EXISTS job_queue (
  id              TEXT PRIMARY KEY,
  plugin          TEXT NOT NULL,
  command         TEXT NOT NULL,
  payload         JSON,
  status          TEXT NOT NULL,
  attempt         INTEGER NOT NULL DEFAULT 1,
  max_attempts    INTEGER NOT NULL DEFAULT 4,
  submitted_by    TEXT NOT NULL,
  dedupe_key      TEXT,
  created_at      TEXT NOT NULL,
  started_at      TEXT,
  completed_at    TEXT,
  next_retry_at   TEXT,
  last_error      TEXT,
  parent_job_id   TEXT,
  source_event_id TEXT,
  event_context_id TEXT,
  enqueued_config_snapshot_id TEXT,
  started_config_snapshot_id TEXT
);

CREATE INDEX IF NOT EXISTS job_queue_status_created_at_idx ON job_queue(status, created_at);
CREATE INDEX IF NOT EXISTS job_queue_plugin_command_status_idx ON job_queue(plugin, command, status);
CREATE INDEX IF NOT EXISTS job_queue_dedupe_status_completed_idx ON job_queue(dedupe_key, status, completed_at);
-- UNIQUE for integrity, not just speed: prevents duplicate child enqueue per
-- (parent, source-event, target). The target dimension (plugin, command) is
-- part of the identity so a single source event fanning out to N distinct
-- consumers enqueues N jobs, while genuine same-target redelivery still
-- dedupes (at-least-once idempotency). Dropping this drops the data-integrity
-- guarantee. C-FRO-16: existing DBs need scripts/migrate-fanout-dedupe-index.py.
CREATE UNIQUE INDEX IF NOT EXISTS job_queue_event_source_idx ON job_queue(parent_job_id, source_event_id, plugin, command) WHERE source_event_id IS NOT NULL;

-- Hickey Sprint 1 branch hickey-sprint-1-job-lineage:
-- append-only job lineage facts. job_queue.status and job_queue.attempt
-- remain the compatibility/cache fields.
-- job_transitions and job_attempts originally declared FOREIGN KEY(job_id)
-- REFERENCES job_queue(id). The direction was structurally backwards: these
-- are append-only fact logs over job_queue mutable status, and the queue
-- row is a cache of the log, not the other way around. The FK blocked the
-- queue retention prune (PruneJobQueue) under foreign_keys=ON because every
-- queue row has children. Dropped 2026-05-05. Existing instances need
-- scripts/migrate-drop-job-fact-fks.py to rebuild without the constraint.
CREATE TABLE IF NOT EXISTS job_transitions (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id      TEXT NOT NULL,
  from_status TEXT,
  to_status   TEXT NOT NULL,
  reason      TEXT,
  created_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS job_transitions_job_created_at_idx ON job_transitions(job_id, created_at);
CREATE INDEX IF NOT EXISTS job_transitions_created_at_idx ON job_transitions(created_at);

CREATE TABLE IF NOT EXISTS job_attempts (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id     TEXT NOT NULL,
  attempt    INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(job_id, attempt)
);

CREATE INDEX IF NOT EXISTS job_attempts_job_created_at_idx ON job_attempts(job_id, created_at);
CREATE INDEX IF NOT EXISTS job_attempts_created_at_idx ON job_attempts(created_at);

-- Hickey Sprint 2 config snapshots:
-- append-only records of successful active runtime config values. Job rows
-- reference the config value that admitted them and the config value that
-- actually started execution. Existing rows may have NULL snapshot IDs.
CREATE TABLE IF NOT EXISTS config_snapshots (
  id                  TEXT PRIMARY KEY,
  config_hash         TEXT NOT NULL,
  source_hash         TEXT,
  source_path         TEXT,
  source              TEXT,
  reason              TEXT NOT NULL,
  loaded_at           TEXT NOT NULL,
  ductile_version     TEXT,
  binary_path         TEXT,
  snapshot_format     INTEGER NOT NULL DEFAULT 1,
  semantics           JSON,
  plugin_fingerprints JSON,
  sanitized_config    JSON,
  secret_fingerprints JSON
);

-- Plugin State: Persistent key-value store for plugins.
CREATE TABLE IF NOT EXISTS plugin_state (
  plugin_name TEXT PRIMARY KEY,
  state       JSON NOT NULL DEFAULT '{}',
  updated_at  TEXT
);

-- Hickey Sprint 7 plugin facts:
-- append-only plugin observations. plugin_state remains the
-- compatibility/current-state row for legacy plugin state reads.
CREATE TABLE IF NOT EXISTS storage_sequences (
  name  TEXT PRIMARY KEY,
  value INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS plugin_facts (
  id          TEXT PRIMARY KEY,
  seq         INTEGER,
  plugin_name TEXT NOT NULL,
  fact_type   TEXT NOT NULL,
  job_id      TEXT NOT NULL,
  command     TEXT NOT NULL,
  fact_json   JSON NOT NULL,
  created_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS plugin_facts_plugin_created_at_idx
ON plugin_facts(plugin_name, created_at);

CREATE INDEX IF NOT EXISTS plugin_facts_plugin_type_created_at_idx
ON plugin_facts(plugin_name, fact_type, created_at);

CREATE INDEX IF NOT EXISTS plugin_facts_plugin_seq_idx
ON plugin_facts(plugin_name, seq);

CREATE INDEX IF NOT EXISTS plugin_facts_plugin_type_seq_idx
ON plugin_facts(plugin_name, fact_type, seq);

-- Event Context: Pipeline execution history and data accumulation.
CREATE TABLE IF NOT EXISTS event_context (
  id               TEXT PRIMARY KEY,
  parent_id        TEXT,
  pipeline_name    TEXT,
  step_id          TEXT,
  accumulated_json JSON NOT NULL,
  created_at       TEXT NOT NULL
);

-- Job Log: Historical record of completed jobs.
CREATE TABLE IF NOT EXISTS job_log (
  id              TEXT PRIMARY KEY,
  job_id          TEXT,
  plugin          TEXT NOT NULL,
  command         TEXT NOT NULL,
  status          TEXT NOT NULL,
  result          TEXT,
  attempt         INTEGER NOT NULL,
  submitted_by    TEXT NOT NULL,
  created_at      TEXT NOT NULL,
  completed_at    TEXT NOT NULL,
  last_error      TEXT,
  stderr          TEXT,
  parent_job_id   TEXT,
  source_event_id TEXT,
  event_context_id TEXT,
  enqueued_config_snapshot_id TEXT,
  started_config_snapshot_id TEXT
);

CREATE INDEX IF NOT EXISTS job_log_job_id_attempt_idx ON job_log(job_id, attempt);
CREATE INDEX IF NOT EXISTS job_log_completed_at_idx ON job_log(completed_at);

-- Circuit Breakers: Fault tolerance state.
CREATE TABLE IF NOT EXISTS circuit_breakers (
  plugin          TEXT NOT NULL,
  command         TEXT NOT NULL,
  state           TEXT NOT NULL DEFAULT 'closed',
  failure_count   INTEGER NOT NULL DEFAULT 0,
  opened_at       TEXT,
  last_failure_at TEXT,
  last_job_id     TEXT,
  updated_at      TEXT NOT NULL,
  PRIMARY KEY(plugin, command)
);

-- Hickey Sprint 4 runtime truth cleanup:
-- append-only circuit breaker history. circuit_breakers remains the
-- compatibility/current-state row used by scheduler decisions.
CREATE TABLE IF NOT EXISTS circuit_breaker_transitions (
  id            TEXT PRIMARY KEY,
  plugin        TEXT NOT NULL,
  command       TEXT NOT NULL,
  from_state    TEXT,
  to_state      TEXT NOT NULL,
  failure_count INTEGER NOT NULL DEFAULT 0,
  reason        TEXT NOT NULL,
  job_id        TEXT,
  created_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS circuit_breaker_transitions_plugin_command_created_idx
ON circuit_breaker_transitions(plugin, command, created_at);

CREATE INDEX IF NOT EXISTS circuit_breaker_transitions_created_at_idx
ON circuit_breaker_transitions(created_at);

-- Job Stopwatch: Supervisor-side timing facts, one row per spawnPlugin
-- invocation. Append-only -- retries produce additional rows distinguished
-- by `attempt`. Written only by the dispatcher (plugins never write here).
-- Telemetry is system data, separate from domain payload (Hickey
-- decomplecting).
--
-- Soft introduction: this table is NOT in the validator requiredTables
-- list, so existing databases continue to start without it. Operators
-- upgrade via scripts/migrate-add-job-stopwatch-table.py. Fresh databases
-- get this from BootstrapSQLite on first start.
CREATE TABLE IF NOT EXISTS job_stopwatch (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id                TEXT    NOT NULL,
  plugin                TEXT    NOT NULL,
  pipeline              TEXT,
  step_id               TEXT,
  pipeline_instance_id  TEXT,
  attempt               INTEGER NOT NULL,
  enter_wall_ns         INTEGER NOT NULL,
  exit_wall_ns          INTEGER NOT NULL,
  dur_ns                INTEGER NOT NULL,
  runtime_pre_ns        INTEGER NOT NULL,
  runtime_post_ns       INTEGER NOT NULL,
  status                TEXT    NOT NULL,
  subs_json             TEXT    NOT NULL DEFAULT '[]',
  recorded_at           TEXT    NOT NULL,
  FOREIGN KEY(job_id) REFERENCES job_queue(id)
);
CREATE INDEX IF NOT EXISTS job_stopwatch_job_attempt_idx
  ON job_stopwatch(job_id, attempt);
CREATE INDEX IF NOT EXISTS job_stopwatch_pipeline_idx
  ON job_stopwatch(pipeline, pipeline_instance_id);

-- Schedule Entries: Last fire times and next scheduled runs.
CREATE TABLE IF NOT EXISTS schedule_entries (
  plugin          TEXT NOT NULL,
  schedule_id     TEXT NOT NULL,
  command         TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'active',
  reason          TEXT,
  last_fired_at   TEXT,
  last_success_job_id TEXT,
  last_success_at  TEXT,
  next_run_at      TEXT,
  updated_at      TEXT NOT NULL,
  PRIMARY KEY(plugin, schedule_id)
);

