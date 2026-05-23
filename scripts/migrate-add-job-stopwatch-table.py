#!/usr/bin/env python3
"""Create the job_stopwatch table on an existing Ductile DB.

WHY THIS EXISTS
---------------
Stopwatch records are supervisor-side timing facts the dispatcher writes
once per spawnPlugin invocation. They live in a dedicated table because
telemetry is system data (about the running of ductile), not domain data
(what plugins operate on); braiding them into request context payload
would violate Hickey decomplecting.

schema.sql includes this table for fresh databases via BootstrapSQLite,
but BootstrapSQLite only runs against empty databases. Existing
deployments must run this script to add the table -- until then, the
dispatcher's writer fails softly (one warn-log per job, no job failure).

WHAT GETS CREATED
-----------------
One table and two indexes. No data is migrated or modified; nothing
existing is touched.

  job_stopwatch                          -- one row per spawnPlugin call
  job_stopwatch_job_attempt_idx          -- lookup by (job_id, attempt)
  job_stopwatch_pipeline_idx             -- lookup by pipeline run

FIELD REFERENCE (job_stopwatch columns)
---------------------------------------
  id                    surrogate PK, auto-increment
  job_id                FK to job_queue.id (the supervised invocation)
  plugin                plugin name that ran
  pipeline              pipeline name, when known (else empty string)
  step_id               pipeline step id, when known
  pipeline_instance_id  per-run id for cross-step attribution
  attempt               1-based retry counter -- retries append rows
  enter_wall_ns         wall-clock entry (correlation only, not duration)
  exit_wall_ns          wall-clock exit (correlation only, not duration)
  dur_ns                monotonic spawn duration -- THE number to compare
  runtime_pre_ns        dispatcher work between request build and spawn
  runtime_post_ns       dispatcher work between spawn return and write
  status                ok | err | timeout | capture_error
  subs_json             plugin-emitted sub-spans as JSON (capped at 32)
  recorded_at           RFC3339Nano UTC of the write

SAFETY
------
- Idempotent. CREATE ... IF NOT EXISTS; re-running after the table exists
  is a metadata no-op.
- Hot-safe under an active gateway. SQLite metadata operations only --
  no table rebuild, no data copy. Brief schema lock held only for the
  DDL transaction; busy_timeout=5000 gives concurrent connections a
  5-second grace if they collide.
- No FK on job_id. job_queue is the mutable hot table whose terminal rows
  get pruned by PruneJobQueue; an FK from this fact log would invert the
  dependency direction and block prune under foreign_keys=ON. Same lesson
  as the earlier migrate-drop-job-fact-fks.py for job_transitions and
  job_attempts. job_id is still the join column, just not DB-enforced.
- Soft introduction: this table is NOT in ductile's startup
  ValidateSQLiteSchema requiredTables list. Forgetting to run the
  migration does not prevent ductile from starting; it only means
  telemetry writes warn-log instead of landing.

DEPLOYMENT
----------
Run once per host against each ductile SQLite file:
  ./scripts/migrate-add-job-stopwatch-table.py /path/to/ductile.db

For matt's deployment, that's:
  Mac dev       ~/.ductile-dev/ductile.db (or wherever cfg.State.Path points)
  Thinkpad dev  ~/.ductile/ductile.db
  Unraid prod   wrapped via unraid_admin/unraid_cmd.sh

After a successful run, the dispatcher's stopwatch warn-logs stop and
rows begin appearing in job_stopwatch on every subsequent plugin
invocation.

EXIT CODES
----------
  0  success (table newly created OR already existed)
  1  db path does not exist
  2  wrong invocation (missing path argument)
"""

import sqlite3
import sys
from pathlib import Path


# DDL applied in order inside one transaction. Order matters: the indexes
# reference the table, so the table CREATE must run first. Each statement
# is independently idempotent (IF NOT EXISTS); the transaction is a
# courtesy that gives all-or-nothing semantics if one DDL were to fail.
SCHEMA_STATEMENTS = [
    # The fact table. One row per dispatcher spawnPlugin invocation.
    # Deliberately no FK on job_id -- see SAFETY note in the module
    # docstring; an FK here would block PruneJobQueue on every row that
    # has telemetry, which would be every row.
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
      recorded_at           TEXT    NOT NULL
    )
    """,
    # Primary inspect lookup: "show me everything for this job (across
    # retry attempts)." Also covers single-job time series for any
    # future aggregation tool.
    """
    CREATE INDEX IF NOT EXISTS job_stopwatch_job_attempt_idx
    ON job_stopwatch(job_id, attempt)
    """,
    # Cross-step attribution: "show me every record for this pipeline
    # run." Enables the gateway_time = wall - sum(dur_ns) calculation
    # without scanning the whole table.
    """
    CREATE INDEX IF NOT EXISTS job_stopwatch_pipeline_idx
    ON job_stopwatch(pipeline, pipeline_instance_id)
    """,
]


def main() -> int:
    # Single positional arg: the SQLite file. We do not auto-discover,
    # to keep the operator in control of which database is touched.
    if len(sys.argv) != 2:
        print(
            "usage: scripts/migrate-add-job-stopwatch-table.py <path-to-sqlite-db>",
            file=sys.stderr,
        )
        return 2

    db_path = Path(sys.argv[1]).expanduser().resolve()
    if not db_path.exists():
        # Fail closed: refuse to create a new (empty) DB. If you reach
        # this branch, you're pointing at the wrong path -- the migration
        # is for existing deployments.
        print(f"db not found: {db_path}", file=sys.stderr)
        return 1

    conn = sqlite3.connect(str(db_path))
    try:
        # PRAGMAs are per-connection and do not change anything for other
        # connections to the same DB.
        #   foreign_keys=ON  : honor FK declarations in our DDL.
        #   journal_mode=WAL : ductile DBs are already WAL; this is a no-op
        #                      reassurance. WAL lets readers continue while
        #                      we write the schema change.
        #   busy_timeout=5000: wait up to 5s on lock contention (e.g. the
        #                      gateway is mid-write) before erroring.
        conn.execute("PRAGMA foreign_keys=ON;")
        conn.execute("PRAGMA journal_mode=WAL;")
        conn.execute("PRAGMA busy_timeout=5000;")

        # All DDL in one transaction. `with conn:` commits on clean exit
        # and rolls back on any exception -- so a partial migration is
        # impossible.
        with conn:
            for stmt in SCHEMA_STATEMENTS:
                conn.execute(stmt)

        # Confirm post-run state so the operator sees the table exists
        # whether this run created it or it was already present.
        row = conn.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='job_stopwatch'"
        ).fetchone()
        if row is None:
            # Should be unreachable given the transaction above succeeded.
            print(f"unexpected: job_stopwatch missing after DDL in {db_path}", file=sys.stderr)
            return 1
        print(f"job_stopwatch present (with indexes) in {db_path}")
        return 0
    finally:
        # Always close. We do not COMMIT here because the `with conn:`
        # block already committed; the close just releases the handle.
        conn.close()


if __name__ == "__main__":
    raise SystemExit(main())
