#!/usr/bin/env python3
"""Create the job_stopwatch_rollup_daily and janitor_heartbeat tables on
an existing Ductile DB.

WHY THIS EXISTS
---------------
The stopwatch janitor pre-aggregates raw job_stopwatch rows into
per-day p50/p90/p99 quartiles before deleting them, so long-term trend
analysis survives the raw-row retention TTL. The janitor also writes a
heartbeat row after each successful tick so the doctor check can warn
when retention is silently failing.

Both tables are present in schema.sql for fresh databases via
BootstrapSQLite, but BootstrapSQLite only runs against empty databases.
Existing deployments must run this script to add the tables -- until
then, the janitor's writes fail softly (warn-log per tick, no job
failure) and retention does not occur.

WHAT GETS CREATED
-----------------
Two tables and four indexes. No data is migrated or modified; nothing
existing is touched.

  job_stopwatch_rollup_daily             -- one row per
                                          --   (plugin, pipeline, step_id,
                                          --    status, day_utc) bucket
    UNIQUE(plugin, pipeline, step_id,    -- prevents double-aggregation
           status, day_utc)              --   on re-run
  job_stopwatch_rollup_pipeline_day_idx  -- lookup by pipeline/step
  job_stopwatch_rollup_plugin_day_idx    -- lookup by plugin

  janitor_heartbeat                       -- one row per named janitor
                                          --   recording its last
                                          --   successful tick

FIELD REFERENCE (job_stopwatch_rollup_daily columns)
----------------------------------------------------
  id              surrogate PK, auto-increment
  plugin          NOT NULL: rollups are always per-plugin
  pipeline        empty string '' when the job had no pipeline context
  step_id         empty string '' when the job had no step context
                  (empty rather than NULL because SQLite UNIQUE treats
                   each NULL as distinct, defeating the constraint)
  status          ok | err | timeout | capture_error -- matches raw table
  day_utc         YYYY-MM-DD in UTC
  sample_count    number of raw rows aggregated into this bucket
  sum_dur_ns      enables mean / re-aggregation
  min_dur_ns      minimum dur_ns in the bucket
  max_dur_ns      maximum dur_ns in the bucket
  p50_dur_ns      50th percentile dur_ns (median)
  p90_dur_ns      90th percentile dur_ns
  p99_dur_ns      99th percentile dur_ns
  created_at      RFC3339Nano UTC of the rollup write

FIELD REFERENCE (janitor_heartbeat columns)
-------------------------------------------
  name              janitor name (PK -- one heartbeat per named janitor)
  last_run_at       RFC3339Nano UTC of the last successful tick
  last_status       ok | err
  last_error        error string from the last tick (empty when ok)
  rows_rolled_up    rows aggregated into the rollup table this tick
  rows_deleted      raw rows deleted this tick

SAFETY
------
- Idempotent. CREATE ... IF NOT EXISTS; re-running after the tables
  exist is a metadata no-op.
- Hot-safe under an active gateway. SQLite metadata operations only --
  no table rebuild, no data copy. Brief schema lock held only for the
  DDL transaction; busy_timeout=5000 gives concurrent connections a
  5-second grace if they collide.
- No FK to job_stopwatch. The rollup OUTLIVES the raw rows by design --
  that is the whole point. An FK would prevent the janitor's prune.

DEPLOYMENT
----------
Run once per host against each ductile SQLite file:
  ./scripts/migrate-add-job-stopwatch-rollup-table.py /path/to/ductile.db

After a successful run, configuring telemetry.stopwatch.rollup.enabled
in the gateway config begins populating the rollup table on each janitor
tick.

EXIT CODES
----------
  0  success (tables newly created OR already existed)
  1  db path does not exist
  2  wrong invocation (missing path argument)
"""

from __future__ import annotations

import sqlite3
import sys
from pathlib import Path


SCHEMA_STATEMENTS: list[str] = [
    """
    CREATE TABLE IF NOT EXISTS job_stopwatch_rollup_daily (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      plugin          TEXT    NOT NULL,
      pipeline        TEXT    NOT NULL DEFAULT '',
      step_id         TEXT    NOT NULL DEFAULT '',
      status          TEXT    NOT NULL,
      day_utc         TEXT    NOT NULL,
      sample_count    INTEGER NOT NULL,
      sum_dur_ns      INTEGER NOT NULL,
      min_dur_ns      INTEGER NOT NULL,
      max_dur_ns      INTEGER NOT NULL,
      p50_dur_ns      INTEGER NOT NULL,
      p90_dur_ns      INTEGER NOT NULL,
      p99_dur_ns      INTEGER NOT NULL,
      created_at      TEXT    NOT NULL,
      UNIQUE(plugin, pipeline, step_id, status, day_utc)
    )
    """,
    """
    CREATE INDEX IF NOT EXISTS job_stopwatch_rollup_pipeline_day_idx
    ON job_stopwatch_rollup_daily(pipeline, step_id, day_utc)
    """,
    """
    CREATE INDEX IF NOT EXISTS job_stopwatch_rollup_plugin_day_idx
    ON job_stopwatch_rollup_daily(plugin, day_utc)
    """,
    """
    CREATE TABLE IF NOT EXISTS janitor_heartbeat (
      name              TEXT PRIMARY KEY,
      last_run_at       TEXT NOT NULL,
      last_status       TEXT NOT NULL,
      last_error        TEXT NOT NULL DEFAULT '',
      rows_rolled_up    INTEGER NOT NULL DEFAULT 0,
      rows_deleted      INTEGER NOT NULL DEFAULT 0
    )
    """,
]


def main() -> int:
    if len(sys.argv) != 2:
        print(
            "usage: scripts/migrate-add-job-stopwatch-rollup-table.py <path-to-sqlite-db>",
            file=sys.stderr,
        )
        return 2

    db_path = Path(sys.argv[1]).expanduser().resolve()
    if not db_path.exists():
        print(f"db not found: {db_path}", file=sys.stderr)
        return 1

    conn = sqlite3.connect(str(db_path))
    try:
        conn.execute("PRAGMA foreign_keys=ON;")
        conn.execute("PRAGMA journal_mode=WAL;")
        conn.execute("PRAGMA busy_timeout=5000;")

        with conn:
            for stmt in SCHEMA_STATEMENTS:
                conn.execute(stmt)

        present = {
            name
            for (name,) in conn.execute(
                "SELECT name FROM sqlite_master WHERE type='table' "
                "AND name IN ('job_stopwatch_rollup_daily', 'janitor_heartbeat')"
            ).fetchall()
        }
        missing = {"job_stopwatch_rollup_daily", "janitor_heartbeat"} - present
        if missing:
            print(f"unexpected: tables missing after DDL in {db_path}: {missing}", file=sys.stderr)
            return 1
        print(f"job_stopwatch_rollup_daily and janitor_heartbeat present in {db_path}")
        return 0
    finally:
        conn.close()


if __name__ == "__main__":
    raise SystemExit(main())
