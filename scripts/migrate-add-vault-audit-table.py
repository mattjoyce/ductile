#!/usr/bin/env python3
"""Create the vault_audit table on an existing Ductile DB.

WHY THIS EXISTS
---------------
vault_audit is an append-only fact log of vault lifecycle operations
(register / set / roll / revoke / purge / dump-values / compose-denial).
It records the op, the principal, the secret NAME, the actor, and the
outcome -- never a secret value. It is observability, not a second copy
of the secret store (Hickey: data about the running of the vault is
system data, separate from the secret model itself).

schema.sql includes this table for fresh databases via BootstrapSQLite,
but BootstrapSQLite only runs against empty databases. Existing
deployments must run this script to add the table -- until then, the
vault owner's audit writer fails softly (one warn-log per op, no op
failure: the secret blob is already saved and is never rolled back for a
missing audit row).

WHAT GETS CREATED
-----------------
One table and two indexes. No data is migrated or modified; nothing
existing is touched.

  vault_audit                            -- one row per vault lifecycle op
  vault_audit_created_at_idx             -- time-ordered queries
  vault_audit_principal_created_idx      -- per-principal history

FIELD REFERENCE (vault_audit columns)
-------------------------------------
  id           surrogate PK, auto-increment
  op           register | set | roll | revoke | purge | dump_values | compose_denial
  principal    principal name, or NULL/'' for secret-scoped ops
  secret_name  secret NAME only -- NEVER a value
  actor        who invoked: e.g. core-admin-token, cli, core
  outcome      ok | denied | error
  detail       non-secret context: denial reason, pattern, skipped count
  created_at   RFC3339Nano UTC of the write

SAFETY
------
- Idempotent. CREATE ... IF NOT EXISTS; re-running is a metadata no-op.
- Hot-safe under an active gateway. SQLite metadata operations only --
  no table rebuild, no data copy. busy_timeout=5000 gives concurrent
  connections a 5-second grace on lock contention.
- No FK on principal/secret_name. These are append-only facts over the
  vault's in-memory age-blob model -- a different store entirely, with no
  shared transaction. An FK would invert the dependency direction.
- Soft introduction: this table is NOT in ductile's startup
  ValidateSQLiteSchema requiredTables list. Forgetting to run the
  migration does not prevent ductile from starting; it only means vault
  audit writes warn-log instead of landing.

DEPLOYMENT
----------
Run once per host against each ductile SQLite file:
  ./scripts/migrate-add-vault-audit-table.py /path/to/ductile.db

EXIT CODES
----------
  0  success (table newly created OR already existed)
  1  db path does not exist
  2  wrong invocation (missing path argument)
"""

import sqlite3
import sys
from pathlib import Path


# DDL applied in order inside one transaction. The table CREATE must run
# before the indexes that reference it. Each statement is independently
# idempotent (IF NOT EXISTS); the transaction gives all-or-nothing
# semantics if one DDL were to fail.
SCHEMA_STATEMENTS = [
    """
    CREATE TABLE IF NOT EXISTS vault_audit (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      op          TEXT NOT NULL,
      principal   TEXT,
      secret_name TEXT,
      actor       TEXT,
      outcome     TEXT NOT NULL,
      detail      TEXT,
      created_at  TEXT NOT NULL
    )
    """,
    """
    CREATE INDEX IF NOT EXISTS vault_audit_created_at_idx
    ON vault_audit(created_at)
    """,
    """
    CREATE INDEX IF NOT EXISTS vault_audit_principal_created_idx
    ON vault_audit(principal, created_at)
    """,
]


def main() -> int:
    if len(sys.argv) != 2:
        print(
            "usage: scripts/migrate-add-vault-audit-table.py <path-to-sqlite-db>",
            file=sys.stderr,
        )
        return 2

    db_path = Path(sys.argv[1]).expanduser().resolve()
    if not db_path.exists():
        # Fail closed: refuse to create a new (empty) DB. The migration is
        # for existing deployments; a missing path means the wrong target.
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

        row = conn.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='vault_audit'"
        ).fetchone()
        if row is None:
            print(f"unexpected: vault_audit missing after DDL in {db_path}", file=sys.stderr)
            return 1
        print(f"vault_audit present (with indexes) in {db_path}")
        return 0
    finally:
        conn.close()


if __name__ == "__main__":
    raise SystemExit(main())
