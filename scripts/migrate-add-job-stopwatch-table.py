#!/usr/bin/env python3
"""Create the job_stopwatch table on an existing Ductile DB.

Stopwatch records are supervisor-side timing facts the dispatcher writes
once per spawnPlugin invocation. They live in a dedicated table because
telemetry is system data (about the running of ductile), not domain data
(what plugins operate on); braiding them into request context payload
would violate Hickey decomplecting.

schema.sql includes this table for fresh databases via BootstrapSQLite,
but BootstrapSQLite only runs against empty databases. Existing
deployments must run this script to add the table — the dispatcher's
writer fails softly (warn-log, no job failure) until then.

Idempotent. CREATE TABLE IF NOT EXISTS + index DDL; re-running after the
table exists is a metadata no-op. Hot-safe under an active gateway
(SQLite metadata operations only, no table rebuild).

Run once per host (Mac dev, Thinkpad dev, Unraid prod) against each
ductile SQLite file.
"""

import sqlite3
import sys
from pathlib import Path


SCHEMA_STATEMENTS = [
    """
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
    )
    """,
    """
    CREATE INDEX IF NOT EXISTS job_stopwatch_job_attempt_idx
    ON job_stopwatch(job_id, attempt)
    """,
    """
    CREATE INDEX IF NOT EXISTS job_stopwatch_pipeline_idx
    ON job_stopwatch(pipeline, pipeline_instance_id)
    """,
]


def main() -> int:
    if len(sys.argv) != 2:
        print(
            "usage: scripts/migrate-add-job-stopwatch-table.py <path-to-sqlite-db>",
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

        print(f"added job_stopwatch table + indexes in {db_path}")
        return 0
    finally:
        conn.close()


if __name__ == "__main__":
    raise SystemExit(main())
