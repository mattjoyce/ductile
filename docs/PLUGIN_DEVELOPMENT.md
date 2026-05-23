# Plugin Development Guide

Ductile is built on a **spawn-per-command** model. A plugin is any executable
that reads one JSON request from `stdin`, writes one JSON response to
`stdout`, and exits. There is no daemon, no shared memory, no in-process
state.

**Durable plugin memory is the append-only `plugin_facts` stream.** A plugin
that needs to remember anything across invocations declares a `fact_outputs`
rule in its manifest, returns a stable snapshot from its durable command,
and lets core record that snapshot append-only and rebuild the compatibility
view automatically. This guide treats the manifest as the contract that
drives plugin quality — every directive is explained below and exists to
push you toward the correct shape. If you find yourself wanting to do
something the manifest doesn't sanction, that is usually a signal to step
back rather than add a workaround.

See [Plugin Facts](./PLUGIN_FACTS.md) for the canonical reference and worked
examples of the durability contract.

---

## 1. The Lifecycle

When a job is triggered (via scheduler, API, or webhook):

1. Ductile forks the plugin entrypoint as a fresh process.
2. The core writes a **request envelope** (JSON) to the plugin's `stdin`.
3. The plugin processes the command and writes a **response envelope**
   (JSON) to `stdout`, then exits.
4. Ductile captures `stderr` for logging and kills the process if it
   exceeds the timeout.

Because every invocation is a fresh process, the plugin has no in-memory
state across calls. Anything the plugin needs to remember must come back
through the request envelope's `state` field on the next invocation.

---

## 2. Protocol v2

### 2.1 Request Envelope (Core → Plugin)

```json
{
  "protocol": 2,
  "job_id": "uuid",
  "command": "poll | handle | health | init",
  "config": {},
  "state": {},
  "context": {},
  "event": {},
  "deadline_at": "ISO8601"
}
```

| Field | What it is |
|---|---|
| `protocol` | The wire-protocol version. Plugins declare which version they expect in `manifest.protocol`; mismatch refuses load. |
| `job_id` | Ductile-assigned unique id for this invocation. Useful in logs and downstream events. |
| `command` | The command the plugin is being asked to run. Always one of `poll`, `handle`, `health`, `init` (plus any plugin-declared command name). |
| `config` | The static plugin config from the operator's YAML, with `${ENV}` interpolated. Read-only. |
| `state` | The plugin's current compatibility-view row — i.e. the latest fact's snapshot for plugins that declare `fact_outputs`, or the protocol-v2 write-through `plugin_state` row for plugins that have not yet migrated. Treat it as *"what I knew last time."* |
| `context` | Shared baggage carried across the pipeline chain. Operator-declared, immutable in the receiving plugin. |
| `event` | Present only for `handle`. The triggering event envelope from upstream. |
| `deadline_at` | Informational ISO8601 timestamp. Plugins may abandon long work early; core enforces the real deadline externally. |

### 2.2 Response Envelope (Plugin → Core)

```json
{
  "status": "ok | error",
  "result": "short human-readable summary",
  "error": "human-readable message (when status=error)",
  "retry": true,
  "events": [],
  "state_updates": {},
  "logs": []
}
```

| Field | What it is |
|---|---|
| `status` | `ok` for success, `error` for any failure. |
| `result` | **Required when `status=ok`.** Short human-readable summary of what happened. Surfaces in `ductile job inspect`, the watch UI, and as the result for synchronous pipelines. |
| `error` | **Required when `status=error`.** Human-readable diagnostic. |
| `retry` | Protocol-v2 compatibility signal. Defaults `true` if omitted. Set `false` only when retrying the same request cannot succeed (configuration error, permanent input invalid). Core owns the final retry decision; this is a *fact about the failure*, not a policy instruction. |
| `events` | Array of `{type, payload, dedupe_key?}` envelopes that drive downstream pipeline routing. |
| `state_updates` | The plugin's emitted **snapshot** for this invocation. When the manifest declares a matching `fact_outputs` rule, core records this snapshot append-only as a `plugin_facts` row and rebuilds the compatibility view (`plugin_state`) from it. See §3.4. |
| `logs` | Array of `{level, message}`. Stored with the job record. |

### 2.3 What `state_updates` Is, And What It Is Not

`state_updates` is the snapshot of the plugin's **observed durable state at
the end of this invocation**. It is not a partial patch and it is not a
running diary of actions taken.

A correct snapshot:

- Is a full object representing the plugin's durable observed state.
- Contains the same keys every invocation that command runs (presence-stable).
- Is deterministic: the same observed inputs produce the same bytes out.
- Has a clear cache-view story: a downstream reader of the latest snapshot
  understands what the plugin knows.

An incorrect snapshot (Sprint 13 anti-patterns — see §6):

- A counter that increments each invocation (`executions_count`).
- A timestamp that updates whether or not anything was observed (`last_run`).
- A diff or patch (`{"new_id": "abc"}`).
- An ordered set built from `set()` union (non-deterministic order).

If a plugin emits action bookkeeping rather than observed state, it should
emit no `state_updates` at all. Action bookkeeping belongs in `job_log`,
which is captured automatically.

### 2.4 Framing And Errors

- One JSON request on stdin → one JSON response on stdout. Not JSON Lines,
  not length-prefixed.
- Exit code `78` (EX_CONFIG) marks a permanent configuration failure and is
  treated as non-retryable regardless of the `retry` field.
- If the request `protocol` field doesn't match what the plugin expects, the
  plugin should exit `78` with a clear error on stderr.

---

## 3. The Manifest (`manifest.yaml`)

The manifest is the single source of truth for what the plugin is, what it
does, and how its memory works. Treat reading this section top-to-bottom as
a quality checklist for any new plugin.

### 3.1 Top-Level Fields

```yaml
manifest_spec: ductile.plugin     # required
manifest_version: 1               # required
name: my_plugin                   # required
version: 0.1.0                    # required
protocol: 2                       # required
entrypoint: run.py                # required
description: "What this plugin does, in one sentence." # optional but recommended
concurrency_safe: true            # optional; default true
commands: [...]                   # required, at least one
fact_outputs: [...]               # required for any plugin with durable memory
config_keys:                      # optional; declares config contract
  required: [...]
  optional: [...]
```

#### `manifest_spec` (required)

Must be the literal string `ductile.plugin`. Identifies this YAML as a
ductile plugin manifest. Future manifest families (e.g. an event spec) would
use a different identifier.

#### `manifest_version` (required)

Must be the integer `1`. Ductile uses this to evolve manifest semantics
accretively without breaking existing plugins.

#### `name` (required)

The plugin's identity. Must be unique across all plugin roots — first plugin
discovered with a given name wins; later duplicates are ignored. Use
underscores or hyphens, no spaces. Pipelines, schedules, and routes refer to
the plugin by this name.

#### `version` (required)

The plugin's release identity over time. Free-form string; prefer
semver-compatible `MAJOR.MINOR.PATCH`. Bump when behaviour changes so
operators can correlate facts and job logs to plugin version.

#### `protocol` (required)

Must be `2`. Declares the wire protocol version this plugin understands.
Mismatch refuses load; do not lie about protocol support.

#### `entrypoint` (required)

Path to the executable, relative to the plugin directory. Must be marked
executable (`chmod +x`). The shebang line picks the interpreter. No `..`
allowed (path traversal prevention). Examples: `run.py`, `run.sh`,
`./bin/dispatcher`.

#### `description` (optional, recommended)

Short human-readable summary of what the plugin does. Surfaces in operator
inspection and LLM-driven tools. Treat it as the answer to *"what does this
plugin do?"* in one sentence.

#### `concurrency_safe` (optional, default `true`)

Concurrency hint. `false` tells the runtime that the plugin is **not** safe
to run two of in parallel — typically because it owns a single-writer
durable resource (a SQLite DB it writes to, an OAuth token table) and
parallel execution would race the writer. When `false`, runtime defaults to
serial execution unless the operator explicitly overrides with
`plugins.<name>.parallelism > 1`.

If you have any doubt, set `false`. Concurrency-safe is a property the
plugin author asserts and the runtime trusts.

#### `commands` (required)

Array of command declarations. Every command the plugin can be invoked with
must be listed, with at least `name` and `type`. See §3.2.

#### `fact_outputs` (recommended for any plugin with durable memory)

Declares which commands emit durable facts and how the compatibility view
is rebuilt. **If your plugin needs to remember anything across invocations,
declare this.** See §3.4.

#### `config_keys` (optional)

Declares the static config contract:

```yaml
config_keys:
  required: [client_id, client_secret, db_path]
  optional: [request_timeout, lookback_days]
```

`required` keys missing at load time refuse the plugin to load. `optional`
keys are documented for operators but not enforced. Keep this list honest —
it is the contract the operator's YAML satisfies.

### 3.2 The `commands` Array

Each command is a pure function on `(config, state, context, event) →
response`. The manifest declares the command's identity, its side-effect
class, its input/output shape, and its retry properties.

```yaml
commands:
  - name: poll
    type: read
    description: "Fetch latest detections; emit one event per first-of-day species."
    idempotent: true
    retry_safe: true
    input_schema: {}
    output_schema:
      status: string
      events: array
      state_updates: object
    values:
      consume: []
      emit:
        - event: birdnet.firstday_species
          values:
            - payload.scientific_name
            - payload.common_name
            - payload.first_id
            - payload.detected_at
```

#### `name` (required)

The command's identity inside this plugin. Standard names that the runtime
recognises: `poll`, `handle`, `health`, `init`. Plugins may declare additional
names (e.g. `token_refresh` in `withings`); those are invocable via API and
schedules but do not get the standard-command convenience routing.

| Standard name | Purpose | Typical `type` |
|---|---|---|
| `poll` | Scheduled durable observation. Emits events on observed change; emits a snapshot in `state_updates`. | `read` |
| `handle` | Event-driven response. Receives an upstream event, processes it, optionally emits downstream events. | `write` (usually) |
| `health` | Diagnostic check. **Emits no `state_updates`.** | `read` |
| `init` | Capability discovery / affordance bundle for LLM tools. **Emits no `state_updates`.** | `read` |

#### `type` (required)

`read` or `write`. This is about **external observable side effects**, not
about whether the command emits durable facts:

- `type: read` — no external POST/PUT/DELETE. Idempotent under retry.
  Examples: `poll`, `fetch`, `get`, `list`, `health`. A `read` command can
  still emit `state_updates` (the durable observation snapshot) and can
  still write to a local SQLite DB the plugin owns; the constraint is on
  external state.
- `type: write` — modifies external state via the network. Examples: `sync`,
  `send`, `notify`, `oauth_callback`, `delete`. Default if `type` is omitted
  (paranoid default).

`type` determines the token scope required to invoke the command
(`plugin:ro` vs `plugin:rw`).

#### `description` (optional)

Short human-readable summary of what this command does. Critical for the
TUI, the watch UI, and LLM operators reading capability discovery.

#### `idempotent` (optional, boolean)

Hint that calling this command N times produces the same observable result
as calling it once, given identical inputs. Used by the runtime to make
safer retry decisions. Be honest: a `sync` that posts measurements to a
remote API is not idempotent unless the remote API deduplicates.

#### `retry_safe` (optional, boolean)

Hint that this command is safe to retry on transient failure. Stronger than
`idempotent` in practice because it accounts for partial-side-effect risk
during retry. Default to `false` if you are unsure.

#### `input_schema` / `output_schema` (optional, legacy)

Either a full JSON Schema object or a compact `field: type` map. Documents
the request payload and response shape for API consumers and operators. The
compact form expands automatically:

```yaml
input_schema:
  url: string
  depth: integer
```

These remain useful as a typed surface but are not the durability contract
— that is `values` plus `fact_outputs`.

#### `values` (optional but recommended)

Names-only payload contract — the *Hickey-faithful* successor to typed
schemas for pipeline authoring. `values.consume` declares which payload
names this command reads from the request. `values.emit` declares, per
emitted event type, which payload names the event carries.

```yaml
values:
  consume:
    - payload.url
    - payload.depth
  emit:
    - event: jina_reader.scraped
      values:
        - payload.url
        - payload.text
        - payload.content_hash
```

Rules:

- Entries are payload **names**, not types. Format: `payload.<key>` or
  `payload.<key>.<sub>` for nested keys; `payload.*` matches all.
- Pipeline authors use `with:` to remap durable context into the request
  payload a downstream plugin expects, and `baggage:` to claim which event
  payload names become durable context. The plugin's `values` declaration
  is a sanity-aid for that authoring.
- `values` does not decide durability. Durability is decided by pipeline
  `baggage:` (for event payloads becoming context) and by `fact_outputs`
  (for `state_updates` becoming `plugin_facts`).

### 3.3 `fact_outputs` — The Durability Declaration

This is the directive that decides whether your plugin participates in the
append-only fact model.

```yaml
fact_outputs:
  - when:
      command: poll
    from: state_updates
    fact_type: my_plugin.snapshot
    compatibility_view: mirror_object
```

A `fact_outputs` rule says: *"when command `poll` succeeds, take its
emitted `state_updates`, record it append-only as a `my_plugin.snapshot`
fact, and rebuild the `plugin_state` row by mirroring the snapshot."*

#### `when.command` (required)

The command name whose successful response produces this fact. One rule per
command-that-emits-durable-state. A plugin may declare multiple rules
(e.g. `withings` declares one for `poll` and one for `token_refresh`,
because both observe durable state).

#### `from` (required)

Currently must be the literal string `state_updates`. The fact is sourced
from the plugin's emitted snapshot. Future protocol versions may add other
sources (e.g. a structured `facts` field in protocol v3); they will be
accretive additions, not breaking changes.

#### `fact_type` (required)

The fact's identity. Convention: `<plugin_name>.<noun>`, where the noun
describes the kind of observation. Most migrated plugins use
`<plugin_name>.snapshot`. Use a different noun only if the plugin emits
distinct kinds of observation that downstream readers should differentiate.

#### `compatibility_view` (optional, default `mirror_object`)

How `plugin_state` is rebuilt from the latest fact. Currently the only
supported value is `mirror_object`: replace `plugin_state.state` wholesale
with the latest fact's `fact_json`. This is exactly what protocol-v2
readers expect, so the migration is transparent.

Future view policies (e.g. a reducer that folds multiple facts) would be
added as new enum values; today, `mirror_object` is the right answer.

### 3.4 The Plugin Memory Model In One Diagram

```
                       plugin emits state_updates snapshot
                                       │
                                       ▼
            ┌───────────────────────────────────────────────┐
            │         core (manifest fact_outputs rule)     │
            └───────────────────────────────────────────────┘
                                       │
                ┌──────────────────────┴──────────────────────┐
                ▼                                             ▼
   plugin_facts (append-only,             plugin_state (compatibility view,
   the durable record):                   rebuilt automatically):
   one row per invocation,                one row per plugin,
   {seq, fact_type, fact_json, ...}       {plugin_name, state, updated_at}
```

The plugin author writes only the snapshot. Core does the rest. The
compatibility view exists so protocol-v2 readers (the request envelope's
`state` field, operator inspection, schedules that read prior state) keep
working without change.

---

## 4. Worked Examples

### 4.1 Minimal Plugin (no durable memory)

A plugin that emits a single event when its `health` is checked. No durable
state, no `fact_outputs` needed.

**`plugins/notify_echo/manifest.yaml`:**

```yaml
manifest_spec: ductile.plugin
manifest_version: 1
name: notify_echo
version: 0.1.0
protocol: 2
entrypoint: run.sh
description: "Emits an echo event when polled. Stateless."
concurrency_safe: true
commands:
  - name: poll
    type: read
    description: "Emits one notify_echo.tick event."
    idempotent: true
    retry_safe: true
    values:
      consume: []
      emit:
        - event: notify_echo.tick
          values:
            - payload.message
            - payload.emitted_at
  - name: health
    type: read
    description: "Reports plugin reachability."
    idempotent: true
    retry_safe: true
config_keys:
  optional: [message]
```

**`plugins/notify_echo/run.sh`:**

```bash
#!/usr/bin/env bash
set -euo pipefail

REQUEST=$(cat)
COMMAND=$(echo "$REQUEST" | jq -r '.command')
MESSAGE=$(echo "$REQUEST" | jq -r '.config.message // "tick"')

case "$COMMAND" in
  poll)
    cat <<EOF
{
  "status": "ok",
  "result": "Emitted notify_echo.tick",
  "events": [{
    "type": "notify_echo.tick",
    "payload": {
      "message": "$MESSAGE",
      "emitted_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    }
  }],
  "logs": [{"level": "info", "message": "Emitted notify_echo.tick"}]
}
EOF
    ;;
  health)
    cat <<EOF
{"status": "ok", "result": "healthy", "logs": [{"level": "info", "message": "ok"}]}
EOF
    ;;
  *)
    cat <<EOF
{"status": "error", "error": "Unknown command: $COMMAND", "retry": false}
EOF
    ;;
esac
```

Notice:

- No `state_updates`. This plugin has no durable memory, so it declares no
  `fact_outputs`.
- `poll` is `type: read` and `idempotent: true` — it observes time and
  emits, with no external side effect.
- `health` is `type: read`, mutates nothing.

### 4.2 Canonical Durable Plugin (poll with snapshot + facts)

A polling plugin that watches a SQLite database, emits an event on each
new row crossing a threshold, and remembers the last result so the next
poll can detect change.

**`plugins/sqlite_change/manifest.yaml`:**

```yaml
manifest_spec: ductile.plugin
manifest_version: 1
name: sqlite_change
version: 0.3.0
protocol: 2
entrypoint: run.py
description: "Polls a SQLite query; emits on threshold crossing."
concurrency_safe: false
commands:
  - name: poll
    type: read
    description: "Run query, emit on threshold crossing, return snapshot."
    idempotent: true
    retry_safe: true
    values:
      consume: []
      emit:
        - event: data.changed
          values:
            - payload.result
            - payload.previous_result
            - payload.detected_at
  - name: health
    type: read
    description: "Report db reachability."
    idempotent: true
    retry_safe: true
fact_outputs:
  - when:
      command: poll
    from: state_updates
    fact_type: sqlite_change.snapshot
    compatibility_view: mirror_object
config_keys:
  required: [db_path, query, event_type]
  optional: [threshold_op, threshold_value, message_template]
```

**`plugins/sqlite_change/run.py`:**

```python
#!/usr/bin/env -S uv run --script
# /// script
# dependencies = []
# ///

import json
import sqlite3
import sys
from datetime import datetime, timezone


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def snapshot_state(*, last_result, last_checked_at, last_triggered_at):
    """Pure constructor for the full compatibility snapshot.

    Every field is required at every call site — the helper never inherits
    silently. Callers that don't observe a field this invocation pass the
    prior state value explicitly.
    """
    return {
        "last_result": last_result,
        "last_checked_at": last_checked_at,
        "last_triggered_at": last_triggered_at,
    }


def poll_command(request):
    config = request.get("config", {})
    state = request.get("state", {})

    # Observe durable state.
    with sqlite3.connect(config["db_path"]) as conn:
        result = conn.execute(config["query"]).fetchone()
    scalar = str(result[0]) if result else None

    timestamp = now_iso()
    triggered = scalar != state.get("last_result")

    events = []
    if triggered:
        events.append({
            "type": config["event_type"],
            "payload": {
                "result": scalar,
                "previous_result": state.get("last_result"),
                "detected_at": timestamp,
            },
        })

    # Build the snapshot. The full compatibility-view shape is emitted every
    # time, even for fields this invocation did not change — the helper
    # guarantees that.
    return {
        "status": "ok",
        "result": f"observed result={scalar} triggered={triggered}",
        "events": events,
        "state_updates": snapshot_state(
            last_result=scalar,
            last_checked_at=timestamp,
            last_triggered_at=timestamp if triggered else state.get("last_triggered_at"),
        ),
        "logs": [{"level": "info", "message": f"polled: {scalar}"}],
    }


def main():
    request = json.load(sys.stdin)
    command = request.get("command")
    if command == "poll":
        response = poll_command(request)
    elif command == "health":
        response = {"status": "ok", "result": "healthy"}
    else:
        response = {"status": "error", "error": f"Unknown command: {command}", "retry": False}
    json.dump(response, sys.stdout)


if __name__ == "__main__":
    main()
```

Notice:

- `fact_outputs` declares `sqlite_change.snapshot` mirrored from `poll`'s
  `state_updates`. That single declaration is what makes core record an
  append-only fact every poll and rebuild `plugin_state` automatically.
- `concurrency_safe: false` because the plugin owns a single-writer
  observation cycle.
- `snapshot_state` is a pure constructor. Every field is explicit at every
  call site — no sentinel-`None` overlay, no implicit inheritance from
  prior state. The caller carries `last_triggered_at` forward by reading it
  from `state` and passing it explicitly.
- The snapshot has the **same three keys every invocation**. A no-change
  poll emits a snapshot byte-identical to the prior one, which keeps
  `plugin_facts` free of reordering noise.
- `health` does not return `state_updates` and does not mutate durable
  state.

---

## 5. The `health` And `init` Pattern

`health` and `init` are diagnostic-only. Neither should emit
`state_updates`. The reasons are concrete:

- `health` runs from the watch UI, the operator's CLI, and circuit-breaker
  half-open probes. If `health` mutated durable state, every diagnostic
  click would create a new fact with no observed change.
- `init` returns an LLM/tool affordance bundle for capability discovery.
  Its output is a function of static metadata, not observed state.

If a plugin's `health` or `init` is currently emitting `state_updates`, that
is a bug — remove the emission. The first post-deploy `poll` or
`token_refresh` will replace `plugin_state` wholesale via `mirror_object`,
sweeping any historical residue.

---

## 6. What Does Not Belong In `state_updates`

These were the explicit non-candidates from the Sprint 13 plugin audit.
None of them should live in `state_updates` or in a `fact_outputs` rule.
They belong in `job_log` (which captures all of them automatically) or
nowhere at all.

| Pattern | Why it's wrong |
|---|---|
| `last_run`, `last_invoked_at` | Action trace. Updates whether or not anything was observed. Use `job_log` for run history. |
| `executions_count`, `total_calls` | Monotonic counter of actions taken. Not observed durable state. |
| `last_pattern`, `last_prompt`, `last_video_id` | Single-field "the most recent thing I did" markers. Diagnostic, not durable observation. |
| `last_summary`, `last_error_message` | Action diagnostics. Belongs in logs. |
| `last_health_check`, `last_init_at` | Diagnostic timestamps from non-mutating commands. |
| Diff or partial-patch shapes | The compatibility view is rebuilt wholesale; partial patches lose information on the next mirror. |
| Lists derived from `set()` union | Non-deterministic order produces a different snapshot on every poll even when nothing changed. |

If your plugin has a candidate field and you're not sure whether it's
observed state or action bookkeeping, ask: *"if a downstream reader reads
this field, are they learning about an external observation, or about my
plugin's own behaviour?"* External observation belongs in the snapshot.
Plugin behaviour does not.

---

## 7. Event Payload Convention

Plugins should follow standard payload field conventions for
interoperability. These are *event* payload conventions, not state
conventions — they live alongside the durability model, not in conflict
with it.

### 7.1 Standard Fields

| Field | Type | Purpose | Used By |
|-------|------|---------|---------|
| `text` | string | Primary text content for processing | **Required** if producing text for downstream steps |
| `result` | string | Final human-readable output | Terminal plugins (fabric, summarizers) |
| `source_url` | string | Originating URL | Web scrapers, YouTube fetchers |
| `source_type` | string | Content origin hint | All plugins |

### 7.2 Source Types

- `web` — web page content (jina-reader)
- `youtube` — YouTube video transcript
- `file` — local file content
- `llm` — LLM-generated content (fabric, claude, etc.)

### 7.3 Event Type Naming

`<plugin_name>.<past_tense_verb>`. Examples:

- `jina_reader.scraped`
- `youtube_transcript.fetched`
- `fabric.completed`
- `file_handler.read`
- `file_handler.written`

### 7.4 Pipeline Integration

The core dispatcher automatically propagates these payload names from input
events to output events:

- `pattern`, `prompt`, `model`
- `output_dir`, `output_path`, `filename`

Plugins **do not** need to manually copy these fields — the dispatcher
handles propagation. Just emit your event with the standard fields and the
pipeline DSL takes care of the rest.

### 7.5 Baggage (Context) Fallback

Only payload names claimed by a pipeline's `baggage:` declaration become
durable context entries in the `event_context` ledger. Downstream plugins
receive that accumulated baggage in `request.context`. If a field may be
produced by an upstream step, prefer:

1. Read from `event.payload` (step-specific input).
2. Fall back to `request.context` for accumulated values.

This makes pipelines resilient when intermediate plugins emit narrower
payloads.

### 7.6 Example Event Payload

```python
return {
    "status": "ok",
    "result": "Scraped https://example.com",
    "events": [{
        "type": "jina_reader.scraped",
        "payload": {
            "url": "https://example.com",
            "source_url": "https://example.com",
            "source_type": "web",
            "text": "Scraped content here...",
            "content_hash": "abc123"
        }
    }]
}
```

---

## 8. Built-in Plugin: `if` Classifier

The `if` plugin is a general-purpose field classifier. It evaluates an
ordered list of checks against a payload field and emits the **first**
matching event type, with the payload unchanged.

### 8.1 Config (per instance)

```yaml
plugins:
  check_youtube:
    enabled: true
    config:
      field: text
      checks:
        - contains: "youtu.be"
          emit: youtube.url.detected
        - contains: "youtube.com"
          emit: youtube.url.detected
        - startswith: "http"
          emit: web.url.detected
        - default: text.received
```

### 8.2 Supported Checks

- `contains`, `startswith`, `endswith`, `equals` (case-insensitive)
- `regex` (Python `re.fullmatch` against the field value)
- `default` (always matches if reached)

### 8.3 Semantics

- Checks are evaluated in order; first match wins.
- Missing fields are treated as empty strings.
- No match + no default → `status: error` with `retry: false`. Core treats
  that as a v2 compatibility signal for a permanent failure.

### 8.4 Instance Naming

Ductile uses manifest names as plugin identities. To create multiple
instances of `if` (or any plugin), use plugin aliasing in `plugins.yaml`:

```yaml
plugins:
  check_youtube:
    uses: if              # inherit the if plugin's implementation
    enabled: true
    config:
      field: text
      checks: [...]
```

The aliased instance has its own config, its own facts, and its own
compatibility-view row.

---

## 9. Filesystem & Diagnostic Bundles

Ductile does not provision a workspace directory for plugins. The core
is dispatch, state, and routing; filesystem is the plugin's concern.

- **If your plugin needs a scratch path,** create it yourself. For
  ephemeral work prefer `mktemp -d` (or the language equivalent) and
  clean up on exit. For persistent caches use `~/.cache/ductile-<plugin>/`
  or a path declared in your plugin config and validated at startup.
- **If your plugin needs an archive of its own stdout** (for offline
  debugging or external log shipping), tee it from inside the run script
  before writing the response envelope, e.g.
  `tee -a "$HOME/.cache/myplugin/stdout.log"`. Core does not write
  subprocess stdout to disk on your behalf; the operationally meaningful
  fragments are already captured in the database (`job_log`,
  `plugin_facts`, `event_context`).
- **Cwd.** Plugin subprocesses inherit the dispatcher's working
  directory. If your plugin cares where it runs, the run script should
  `cd` to a path of its own choosing.

## 10. Security & Isolation

- **Allowed paths.** Plugins should only read/write paths they
  themselves create (per the previous section) or paths explicitly
  named in their config.
- **Execution.** Plugins run as the same OS user as the gateway. Use
  filesystem permissions to limit blast radius.
- **Trust.** Ductile refuses to load plugins with world-writable directories
  or `..` in their `entrypoint`. The entrypoint must be `chmod +x`.
- **No persistent state outside what is declared.** Plugins must not write
  to their own plugin directory at runtime. Anything durable goes through
  `state_updates` (subject to the manifest's `fact_outputs` rule); anything
  ephemeral goes to a plugin-managed scratch path.

---

## 11. Quick Quality Checklist

When you finish a new plugin, walk this list before merging:

- [ ] `manifest_spec`, `manifest_version`, `name`, `version`, `protocol: 2`,
      `entrypoint` set.
- [ ] `description` is a real one-sentence summary.
- [ ] `concurrency_safe` is honestly set (`false` if the plugin owns a
      single-writer durable resource).
- [ ] Every command has `name`, `type`, `description`, and honest
      `idempotent` / `retry_safe` flags.
- [ ] Standard commands (`poll`, `handle`, `health`, `init`) follow the
      conventions in §3.2.
- [ ] `health` and `init` emit no `state_updates`.
- [ ] Each command declares `values.consume` / `values.emit` so pipeline
      authors can see the contract.
- [ ] If the plugin remembers anything across invocations, it declares
      `fact_outputs` for the durable command(s).
- [ ] The emitted snapshot is a full object, deterministic, and has the
      same keys every invocation of that command (presence-stable).
- [ ] Nothing in `state_updates` matches the §6 anti-patterns.
- [ ] `config_keys.required` is honest — required keys must actually be
      required.
- [ ] Entrypoint is `chmod +x`.
- [ ] Tests cover the snapshot constructor and the response shape.

If every box ticks, the plugin is aligned with the durability model and
should not need a future migration sprint to fix.

---

## Stopwatch — timing is captured for you

The dispatcher times every plugin invocation automatically. You do not need
to wrap your handler in `time.now()` calls; the supervisor records a
`stopwatch.Record` (plugin id, step, monotonic duration, status, etc.) into
the request context under the reserved key `ductile_stopwatch`. This is the
single right place to read or aggregate per-step timing — see
[PLUGIN_DIAGNOSTICS.md](./PLUGIN_DIAGNOSTICS.md) for the data shape and the
`gateway_time` formula.

`ductile_stopwatch` is **system-owned**. Any value a plugin places under
that key in its response or state is overwritten by the dispatcher; do not
rely on it as a free-form storage slot.

### Optional sub-spans for plugin-internal phases

If you want to break down what your handler did internally (`db_query`,
`http_call`, parsing, etc.), emit a list under `ductile_stopwatch_subs` at
the top level of your response. Shape:

```json
{
  "status": "ok",
  "result": "...",
  "ductile_stopwatch_subs": [
    {"name": "db_query", "dur_ns": 11000000},
    {"name": "http_call", "dur_ns": 31000000}
  ]
}
```

Rules:

- Sub-spans are advisory. The dispatcher's own Record is always emitted
  regardless of whether you include sub-spans.
- The dispatcher caps the list at 32 entries per invocation. If you emit
  more, the excess is dropped and one warning is logged for the call.
- Malformed entries (non-object items, non-list values) are dropped
  silently. Defensive parsing is part of the contract.
- The dispatcher does not interpret sub-span fields beyond storing them.
  You choose the key names; `name` and `dur_ns` are conventional.
