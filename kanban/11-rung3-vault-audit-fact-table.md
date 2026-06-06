---
id: 11
status: done
priority: Normal
blocked_by: [10]
tags: [vault, rung3, storage, audit]
---

# Rung 3 · `vault_audit` fact table

A **net-new** append-only audit fact table for vault operations.

**Scope:**
- Add a `vault_audit` table to `internal/storage/schema.sql` (none exists today).
- Record: register / set / roll / revoke / purge / dump-values / compose-denial events with principal, secret name (not value), timestamp, actor.
- Feeds `dump --values` audit-logging (#10, handoff §dump policy) and security observability.

**Acceptance:** lifecycle ops append audit rows; secret **values never** recorded; rows queryable.

## Narrative
- **Source:** handoff §"Build sequence — Rung 3" ("net-new `vault_audit` fact table … none exists in `internal/storage/schema.sql`").

### Done (2026-06-02) — commit 8baec7c
- **Storage:** `vault_audit` table + indexes (`vault_audit_created_at_idx`, `vault_audit_principal_created_idx`)
  in `schema.sql`, following the append-only fact-table idiom (`job_stopwatch`). **Soft-introduced** — NOT
  in `ValidateSQLiteSchema` requiredTables, so existing DBs keep starting; `scripts/migrate-add-vault-audit-table.py`
  for upgrades (idempotent, smoke-tested). Records op, principal, secret **name**, actor, outcome, detail — never a value.
- **State:** `state.AppendVaultAudit` (parameterized INSERT) + `state.ListVaultAudit` (newest-first), mirroring
  `RecordStopwatch`'s best-effort fault model.
- **Emission placement (Hickey/Armstrong):** at the **call sites**, not the vault. The actor lives at the entry
  surface (admin-token / core), so the `Vault` owner and the pure `Store` stay **completely untouched**. API:
  a `VaultAuditor` narrow interface (satisfied by `*state.Store`) wired via `api.Config`; all 7 authenticated
  `/vault/*` mutations emit a fact. Dispatch: the fail-closed `compose_denial` records a fact (actor=core).
  Audit-write failure logs loudly but never rolls back the op (the blob is already saved; the response is 200).
- **dump-values (ISC-16): N/A** — there is no `dump` verb in the CLI yet (#10's dump policy isn't built). The
  `dump_values` op vocabulary + the table are ready for when it lands.
- **Gate:** gofmt/vet clean; `go test -race` green on state/storage/api/dispatch (shuffle on state/storage/api).
  Tests assert a fact per op and a full-column scan that no secret value is ever stored.
- **Hardware e2e:** to be run combined with #12 attestation in one Dell pass (per the rung-3 e2e pattern).
