# Plugin Facts

This document is the canonical reference for durable plugin memory in Ductile.

**The model:** durable plugin truth is the append-only `plugin_facts` stream.
`plugin_state` is a compatibility/cache view of the latest fact, kept current
automatically by core so that legacy readers see the same shape they
always have. New plugins declare `fact_outputs` in their manifest and let the
view come for free.

## 1. What A Plugin Does

A plugin that needs to remember anything across invocations follows this
pattern:

1. The plugin emits a successful, stable snapshot in `state_updates`.
2. The plugin manifest declares that snapshot as a fact output.
3. Core records the snapshot as an append-only row in `plugin_facts`, with a
   Ductile-owned monotonic `seq` and the declared `fact_type`.
4. Core rebuilds the compatibility `plugin_state` row from the newest fact
   according to the declared `compatibility_view` (currently `mirror_object`).

This means:
- `plugin_facts` is the durable record.
- `plugin_state` is the compatibility/cache view.

```historical-note
Previously, plugins wrote durable truth directly into `plugin_state` via
shallow merge of `state_updates`. That model is legacy; new plugins should
always declare `fact_outputs`. Plugins still on direct write-through are
running in a compatibility window.
```

## 2. Migrated Plugins

In-tree (codex repo):
- `file_watch` `poll` → `file_watch.snapshot`
- `folder_watch` `poll` → `folder_watch.snapshot`
- `py-greet` `poll` → `py-greet.snapshot`
- `ts-bun-greet` `poll` → `ts-bun-greet.snapshot`
- `stress` `state` → `stress.state_snapshot`

External plugins:
- `gmail_poller` `poll` → `gmail_poller.snapshot`
- `youtube_playlist` `poll` → `youtube_playlist.snapshot`
- `jina-reader` `poll` → `jina-reader.snapshot`
- `birdnet_firstday` `poll` → `birdnet_firstday.snapshot`
- `sqlite_change` `poll` → `sqlite_change.snapshot`
- `withings` `poll` and `token_refresh` → `withings.snapshot`

`health` commands are intentionally **not** part of the durable fact flow.
Health is diagnostic and should not mutate durable state.

## 3. Compliance Rules

If you want a plugin to be compatible with this pattern, the plugin and core
need a clear, defensible contract.

### Plugin-side rules

- Emit facts only from commands that produce meaningful durable truth.
- Prefer successful `poll` or equivalent snapshot-producing commands.
- Do not use `health` or `init` as durable state — they should emit no `state_updates`.
- Keep the emitted snapshot shape stable and explicit.
- Return a full snapshot, not a partial patch. The compatibility view is
  rebuilt wholesale from the latest fact, so partial patches lose information.
- Keep the snapshot JSON object-shaped and deterministic enough for operators
  to inspect; avoid non-deterministic ordering inside lists or maps.

### Core-side rules

- Declare an explicit fact type in `manifest.yaml`.
- Record each fact append-only in `plugin_facts`.
- Declare how compatibility `plugin_state` is derived from that fact.
- Add an operator-visible read path.
- Add tests that prove both fact persistence and derived compatibility state.

The smallest useful manifest shape is:

```yaml
fact_outputs:
  - when:
      command: poll
    from: state_updates
    fact_type: file_watch.snapshot
    compatibility_view: mirror_object
```

## 4. Recommended Fact Shape

Use a fact when the plugin can answer:

> "What is the current durable observed state of this plugin right now?"

Good candidates:
- watcher snapshots
- cursors/checkpoints
- discovered remote resource inventories
- reducer-friendly state snapshots

Poor candidates:
- transient health checks
- ephemeral timing/latency noise
- values that are meaningful only to a single in-flight job

For a first migration, prefer a **full snapshot** over incremental diffs.

## 5. Snapshot Examples

### `file_watch`

`file_watch poll` returns a snapshot shaped like:

```json
{
  "watches": {
    "single-file": {
      "exists": true,
      "fingerprint": "abc123",
      "size": 42,
      "mtime_ns": 1713740000000000000,
      "path": "/tmp/file.txt",
      "strategy": "sha256",
      "updated_at": "2026-04-22T01:02:03Z"
    }
  },
  "last_poll_at": "2026-04-22T01:02:03Z"
}
```

Core then:
- stores that JSON in `plugin_facts.fact_json`
- assigns a Ductile-owned `plugin_facts.seq` for new facts
- tags it `file_watch.snapshot`
- updates `plugin_state` for `file_watch` to the same snapshot shape

This keeps legacy state readers working while giving operators an append-only
history.

### `folder_watch`

`folder_watch poll` returns the same top-level compatibility shape:

```json
{
  "watches": {
    "docs": {
      "root": "/srv/content",
      "files": {
        "summary.md": "abc123"
      },
      "snapshot_hash": "def456",
      "file_count": 1,
      "updated_at": "2026-04-22T01:02:03Z"
    }
  },
  "last_poll_at": "2026-04-22T01:02:03Z"
}
```

### `py-greet` and `ts-bun-greet`

The example greeting plugins emit a tiny full snapshot:

```json
{
  "last_run": "2026-04-22T01:02:03Z",
  "last_greeting": "Hello, Ductile!"
}
```

### `stress`

The `stress state` command emits the full compatibility snapshot for its only
durable datum:

```json
{
  "count": 42
}
```

## 6. Migration Checklist For Another Plugin

When migrating another plugin to `plugin_facts`, do all of the following:

1. Choose one command that produces durable truth.
2. Define one explicit fact type.
3. Make the plugin emit a stable object snapshot.
4. Ensure the snapshot is a full compatibility-state view, not just a partial
   patch.
5. Add `fact_outputs` to the plugin manifest.
6. Declare the compatibility view policy for `plugin_state`.
7. Add operator inspection support.
8. Add unit tests for persistence and derived state.
9. Add a Docker or similarly realistic fixture when runtime behavior matters.
10. Document the fact type, snapshot shape, and non-goals.

## 7. Questions To Resolve Before Adding A New Plugin Or Migrating One

Before declaring `fact_outputs` for a plugin, answer:

- What exact command owns durable truth?
- Is the emitted JSON a full snapshot or only a delta? It must be a full
  snapshot — partial patches break the compatibility view.
- Should the compatibility view mirror the newest fact exactly
  (`compatibility_view: mirror_object`), or does the plugin need a different
  reduction policy? Today only `mirror_object` is supported; a reducer-based
  policy would be a future extension.
- What data should remain diagnostic only and stay out of durable storage?
- How will an operator inspect recent facts?
- What realistic test proves the fact path end to end?

If those answers are vague, the plugin should remain on direct
write-through (action-bookkeeping non-candidates) rather than declaring a
half-thought fact contract.

## 8. Deployment Note

For existing databases, apply required schema migrations before a normal deploy,
then restart or deploy the updated binary.

For non-empty existing databases, startup should validate and fail if required
schema is missing. It should not silently add `plugin_facts`, `seq`, or related
indexes during normal open. Startup errors should name the migration script
needed for the current database shape.

Existing rows without `seq` keep `seq` as `NULL`. Ductile does not backfill
guessed order for legacy facts; new rows use `seq` for ordering, and legacy rows
fall back to their previous timestamp order.
