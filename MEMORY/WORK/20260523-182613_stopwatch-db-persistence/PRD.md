---
task: Persist stopwatch records to ductile DB, decomplect from baggage
slug: 20260523-182613_stopwatch-db-persistence
effort: advanced
phase: complete
progress: 32/32
mode: interactive
started: 2026-05-23T08:26:13Z
updated: 2026-05-23T08:26:13Z
bead: claude-y98
supersedes_in: claude-caq
branch: feature/claude-caq-stopwatch
---

## Context

Continuation of the Stopwatch work. The MVP (claude-caq) attached Records to `requestContext` which the dispatcher merges into `event.Payload` at `dispatcher.go:385-389` — a Hickey-decomplecting violation: supervisor observations braided with plugin domain payloads.

This bead implements the correct shape: a dedicated `job_stopwatch` table the supervisor writes to and inspect reads from. Records live in the ductile DB indexed by job_id (and pipeline_instance_id for cross-step attribution). No travel via baggage. No payload pollution. No AccumulatedJSON size pressure.

Follows the established convention: append-only fact table in `schema.sql` + corresponding `scripts/migrate-*.py` Python script (idempotent CREATE TABLE IF NOT EXISTS).

### Hickey / Armstrong principles applied

- **Decomplecting (Hickey):** Records are system data, not domain data. Their place is the supervisor's ledger (DB), not the inter-process message channel.
- **Supervisor's ledger (Armstrong):** Observability belongs to the supervisor. The DB is the supervisor's persistent ledger. Plugins receive no view; inspect queries the ledger.
- **Let it crash, supervisor stays up (Armstrong):** Writer failures log via jobLogger but never propagate to the job — losing telemetry is acceptable; losing the job because telemetry storage failed is not.
- **Value of values (Hickey):** The Record value is unchanged from claude-caq; only its destination changes. Immutable, comparable, JSON-serializable. The `subs_json` column stores the Subs slice as JSON (a value-of-values).

## Criteria

### Schema

- [x] ISC-1: job_stopwatch table added to internal/storage/schema.sql
- [x] ISC-2: Table has all required columns (id, job_id, plugin, pipeline, step_id, pipeline_instance_id, attempt, enter_wall_ns, exit_wall_ns, dur_ns, runtime_pre_ns, runtime_post_ns, status, subs_json, recorded_at)
- [x] ISC-3: Index job_stopwatch_job_attempt_idx exists on (job_id, attempt)
- [x] ISC-4: Index job_stopwatch_pipeline_idx exists on (pipeline, pipeline_instance_id)

### Migration script

- [x] ISC-5: scripts/migrate-add-job-stopwatch-table.py exists
- [x] ISC-6: Script is executable (file mode 0755)
- [x] ISC-7: Script uses CREATE TABLE IF NOT EXISTS (idempotent)
- [x] ISC-8: Script docstring explains what, why, idempotency
- [x] ISC-9: Script accepts single arg <path-to-sqlite-db> and validates existence

### Go state writer

- [x] ISC-10: internal/state/stopwatch.go exists with RecordStopwatch method
- [x] ISC-11: Method signature is ctx-aware and accepts a job reference plus a stopwatch.Record
- [x] ISC-12: Method writes all Record fields to job_stopwatch
- [x] ISC-13: Method serializes Record.Subs as JSON to subs_json column
- [x] ISC-14: Method sets recorded_at to time.Now().UTC() in RFC3339Nano
- [x] ISC-15: Writer returns error on failure but does not panic

### Dispatcher rewire

- [x] ISC-16: Dispatcher calls state.RecordStopwatch after sw.Finish
- [x] ISC-17: stopwatch.Attach call removed from dispatcher
- [x] ISC-18: Writer errors logged via jobLogger; no propagation that would fail the job

### Stopwatch package cleanup

- [x] ISC-19: stopwatch.Attach function removed
- [x] ISC-20: stopwatch.ContextKey constant removed
- [x] ISC-21: Package doc updated: supervisor writes to ductile DB, no context namespace

### Tests

- [x] ISC-22: state package gains test proving a record round-trips through RecordStopwatch
- [x] ISC-23: Dispatcher integration test asserts a row exists in job_stopwatch after executeJob
- [x] ISC-24: Integration test asserts row.status equals "ok" for happy-path execution

### Docs

- [x] ISC-25: PLUGIN_DIAGNOSTICS.md updated: query via DB / inspect, no "request context" language
- [x] ISC-26: PLUGIN_DEVELOPMENT.md updated: drop "ductile_stopwatch is system-owned context key" line

### Quality gates

- [x] ISC-27: go vet ./... clean
- [x] ISC-28: go test ./... green across all packages
- [x] ISC-29: Existing inspect tests still pass

### Anti-criteria (must NOT happen)

- [x] ISC-A1: protocol.Response.StopwatchSubs NOT removed (plugin sub-spans still ingested)
- [x] ISC-A2: No schema/FK changes to unrelated tables
- [x] ISC-A3: NO new module dependencies beyond Go standard library

## Decisions

- **Append-only table.** One row per attempt. Retries produce additional rows distinguished by `attempt`. Same shape philosophy as plugin_facts, job_attempts, job_transitions.
- **FK to job_queue(id)?** YES. The Sprint-1 lineage tables use FK; the more recent drop-fk migration applied only to plugin_facts. New lineage-style tables continue to use FK. Cheap integrity, no operational cost.
- **subs_json as TEXT.** Store the slice as a JSON string. Simpler than a separate sub-spans table; reads are by job_id anyway and the cap (32 entries) keeps blob size bounded.
- **Writer non-propagating.** Armstrong: telemetry failure must not crash the job. Log via jobLogger, return error from the writer method, dispatcher swallows after logging.
- **Migration script name.** `scripts/migrate-add-job-stopwatch-table.py` — descriptive, not sprint-numbered (the codebase has both patterns; sprint numbering is for branded sprint work, this is just adding a table).

## Verification

**Build + tests:**
- `go build ./...` — clean
- `go vet ./...` — clean
- `go test ./...` — green across 24 packages
- New tests: 3 in `internal/state/stopwatch_test.go`, 2 in `internal/dispatch/dispatcher_stopwatch_test.go` (real subprocess plugins → real SQLite → row assertion)

**Code review (low-effort skill pass on diff):** no runtime-correctness findings.

**Bugs caught + fixed mid-execution:**
- Import cycle (queue ↔ state): refactored RecordStopwatch to take primitives instead of `*queue.Job`.
- SQL-comment semicolon: bootstrap parser splits by `;` without comment-awareness; removed the semicolon from the schema.sql comment block.

**Hickey / Armstrong audit:**
- Telemetry decomplected from baggage: lives in `job_stopwatch`, never in `request.Context`.
- Supervisor's ledger (DB) is the single source of truth; plugins have no view.
- Writer non-propagating: `jobLogger.Warn` on failure, no job-fail.
- Append-only: one row per attempt; immutable values.
- `time.RFC3339Nano` matches the codebase convention.

**Deferred to claude-9mf:** inspect surface (now a straightforward SQL read).
