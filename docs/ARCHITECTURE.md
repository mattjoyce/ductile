# Ductile — Specification

**Version:** 1.0
**Date:** 2026-02-08
**Author:** Matt Joyce
**Sources:** RFC-001, RFC-002, RFC-002-Decisions

This is the unified, buildable specification for Ductile. It supersedes all prior RFCs and review documents.

---

## 1. Overview

### 1.1 Problem

Ductile currently exists as a FastAPI monolith handling health data ETL, LLM processing, and various integrations. Adding new connectors means modifying the core application. Existing integration servers (n8n, Huginn, Node-RED) are too heavy for a personal service.

### 1.2 Solution

An automation runtime built for AI agents to operate, diagnose, and extend. Where platforms impose workflow, Ductile provides primitives: a NOUN ACTION CLI, a manifest-contracted plugin protocol, and a queryable execution ledger. A compiled Go core orchestrates polyglot plugins via a subprocess protocol. Simple enough for a human to understand in an afternoon; structured enough for an agent to drive the full lifecycle without supervision. See [`../CONSTITUTION.md`](../CONSTITUTION.md).

### 1.3 Scope

This is a **personal integration server** processing roughly 50 jobs per day. Design decisions are calibrated to that scale. The system runs unattended and must behave predictably under crash, retry, and timeout conditions.

---

## 2. Architecture

```
┌────────────────────────────────────────────────────────────────────────┐
│                                ductile                                 │
│                        (Go binary, ~1 process)                         │
│                                                                        │
│   INGRESS (Producers)                                                  │
│   ┌───────────┐      ┌──────────┐      ┌──────────┐      ┌───────────┐ │
│   │ Scheduler │      │ Webhook  │      │   API    │      │    CLI    │ │
│   │ (heartbeat)      │ Receiver │      │  Server  │      │  Commands │ │
│   └─────┬─────┘      └────┬─────┘      └────┬─────┘      └─────┬─────┘ │
│         │                 │                 │                  │       │
│         ▼                 ▼                 ▼                  ▼       │
│   ┌──────────────────────────────────────────────────────────────────┐ │
│   │                     SQLite WORK QUEUE (FIFO)                     │◄┼┐
│   │      (Single source of truth & transactional durability)         │ ││
│   └──────────────────────────────┬───────────────────────────────────┘ ││
│                                  │                                     ││
│                                  ▼                                     ││
│   ┌──────────────────────────────────────────────────────────────────┐ ││
│   │                        DISPATCHER LOOP                           │ ││
│   │   pull job → execute subprocess → collect result → route events   │ ││
│   └─────────────────┬────────────────────────────────────────────────┘ ││
│                     │                                                  ││
│                     │ plugin stdout                                    ││
│                     ▼                                                  ││
│             [ emitted events? ]                                        ││
│                     │                                                  ││
│            yes      ▼                                                  ││
│         ┌───────────┴──────────┐                                       ││
│         │     Event Router     ├─ (calculates downstream dispatches) ──┘│
│         │  (Pipeline Engine)   │                                        │
│         └──────────────────────┘                                        │
│                     │                                                   │
│                     │ observation/telemetry                           │
│                     ▼                                                   │
│         ┌──────────────────────┐        ┌──────────────────────┐        │
│         │      Event Hub       ├───────►│    TUI / Streaming   │        │
│         │ (Observation Pub/Sub)│        │    API Observers     │        │
│         └──────────────────────┘        └──────────────────────┘        │
│                                                                        │
│   ┌──────────┐      ┌──────────┐      ┌────────────┐                   │
│   │  Config  │      │  State   │      │   Plugin   │                   │
│   │  Loader  │      │  Store   │      │  Registry  │                   │
│   │  (YAML)  │      │ (SQLite) │      │            │                   │
│   └──────────┘      └──────────┘      └────────────┘                   │
└──────────────────────────────────┬─────────────────────────────────────┘
                                   │ stdin/stdout JSON protocol
                     ┌─────────────┼─────────────┐
                     ▼             ▼             ▼
                ┌─────────┐  ┌──────────┐  ┌─────────┐
                │withings/ │  │ google/  │  │ notify/ │
                │ run.py   │  │ run.py   │  │ run.sh  │
                └─────────┘  └──────────┘  └─────────┘
```

### 2.1 Key Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Core language | Go | Single binary, easy deployment, natural subprocess spawning |
| Plugin coupling | Subprocess (JSON over stdin/stdout) | Language-agnostic, fault-isolated, drop-in plugins |
| Scheduling | Heartbeat with fuzzy intervals | Human-friendly, avoids thundering herd |
| Execution | Bounded Worker Pool | High-throughput, resource-safe, per-plugin concurrency caps |
| Routing | Config-declared, queue-centric, exact match | Plugins stay dumb; the core controls flow via the Work Queue |
| Pipeline Execution | Async by default; Sync opt-in | Preserves event-driven core while enabling interactive results |
| State | SQLite | Proven, zero-ops; append-only `plugin_facts` with derived compatibility view |
| Delivery | At-least-once | Plugins own idempotency; core never drops work |
| Plugin lifecycle | Spawn-per-command | Eliminates daemon management, memory leaks, zombie processes |

### 2.2 Execution Boundaries: Queue-Centric vs. Event Bus

To maintain fault isolation, transactional durability, and predictable recovery, Ductile enforces a strict separation between **Core Execution** and **Observation**:

*   **Core Execution is strictly Queue-Centric and Router-Driven**:
    *   Ductile does *not* utilize an active in-memory "event bus" for routing execution between plugins.
    *   Instead, the **SQLite-backed Work Queue** serves as the transactional boundary. When a plugin completes and emits events, the **Event Router** (`internal/router`) deterministically evaluates active pipelines/global routing rules, calculates downstream dispatches, and enqueues them immediately back to the SQLite Work Queue. 
    *   This design guarantees that every execution transition is durable. If the service crashes or is restarted mid-pipeline, the exact state is recovered from the SQLite Work Queue on startup, avoiding lost messages.
*   **Observation is Pub/Sub-Driven via the Event Hub**:
    *   For live diagnostics and TUI updates, the **Event Hub** (`internal/events`) provides a lightweight, in-memory pub/sub ring-buffer.
    *   The Dispatcher publishes passive lifecycle telemetry events (e.g. `job.succeeded`, `router.enqueued`) to the Event Hub.
    *   This observation stream is completely decoupled from execution. Slow or blocked subscribers (such as a stalled TUI client or a streaming API consumer) can never block core workers or delay job execution.

### 2.3 Core Component Roles

1.  **Ingress Gateway (Producers)**: Handles external and internal triggers. Includes the **Scheduler** (initiating scheduled poll jobs), the **Webhook Receiver** (verifying HMAC signatures on inbound HTTP payloads), the **API Server** (accepting programmatic job/pipeline triggers), and the **CLI**. All producers write transactionally to the SQLite Work Queue.
2.  **SQLite Work Queue**: The single, durable source of truth. Manages job FIFO sequencing, attempts, retries, and deduplication states.
3.  **Dispatcher**: The bounded worker pool consumer. Dequeues eligible jobs, spawns isolated polyglot plugin subprocesses (communicating via protocol v2), enforces command timeouts, and captures execution results.
4.  **Event Router (Pipeline & Event Routing Engine)**: The deterministic routing coordinator. Interprets event payloads emitted by plugins and matches them against YAML-defined DSL pipelines to generate downstream queue enqueues.
5.  **Event Hub**: An observational sidecar providing real-time pub/sub streams of telemetry events for external diagnostic clients.
6.  **State Store**: Backed by SQLite, maintains both the append-only `plugin_facts` observed history and the auto-derived `plugin_state` compatibility cache views.

### 2.4 Governance Hybrid (The "Control Plane")

Ductile employs a "Governance Hybrid" model to manage state across
multi-hop plugin chains. Filesystem state is the plugin's concern; the
core is dispatch, routing, and durable state.

*   **Control Plane (Baggage):** Metadata about the execution (e.g.,
    `origin_user_id`, `trace_id`). This data is stored in the
    `event_context` SQLite ledger. Values become durable only when a
    pipeline step claims them with `baggage`, and inherited baggage
    paths are immutable.
*   **No core-managed data plane.** The core does not provision per-job
    filesystem workspaces. Plugins that need a
    scratch path (`mktemp -d`) or a persistent cache
    (`~/.cache/<plugin>/`) manage it themselves; pipelines that need
    step-to-step file passing wire absolute paths via `with:` baggage.
    See `docs/PLUGIN_DEVELOPMENT.md` §9 for guidance.

---

## 3. Work Queue

The central abstraction. All producers submit to a single queue.

### 3.1 Producers

| Producer | Trigger |
|----------|---------|
| Scheduler | Heartbeat tick finds a plugin is due |
| Webhook receiver | Inbound HTTP event |
| Router | Plugin output matches a routing rule |
| CLI | Manual `ductile run <plugin>` |

### 3.2 Job Model

```
{
  id:              UUID
  plugin:          string
  command:         string (poll | handle)
  payload:         JSON
  status:          queued | running | succeeded | failed | timed_out | dead
  attempt:         int (starts at 1)
  max_attempts:    int (default 4)
  submitted_by:    string (scheduler | webhook | route | cli)
  dedupe_key:      string (optional)
  created_at:      timestamp
  started_at:      timestamp (null until running)
  completed_at:    timestamp (null until terminal)
  next_retry_at:   timestamp (null unless awaiting retry)
  last_error:      text (null unless failed)
  parent_job_id:   UUID (null unless created by routing)
  source_event_id: UUID (null unless created by routing)
}
```

No `priority` field. Jobs are strictly FIFO.

### 3.3 Job State Machine

```
queued → running → succeeded
                 → failed → queued (retry)
                          → dead (max retries exceeded)
         → timed_out → queued (retry)
                     → dead (max retries exceeded)
```

### 3.4 Delivery Guarantee

**At-least-once.** A job may run more than once (after crash, timeout, or retry). It will never be silently dropped.

- Plugins MUST be idempotent, or use `state` to track what they've already processed.
- The core provides an opt-in `dedupe_key` field. If a job is enqueued with a `dedupe_key` matching a job that succeeded within the effective dedupe window, it is not enqueued. The drop is logged at `INFO` with the `dedupe_key` and existing job ID.
- `dedupe_ttl` is configurable (default 24h) and acts as the default dedupe window. Callers may set a per-enqueue dedupe TTL override when a narrower window is needed (for example, scheduler cadence). When this override is set, enqueue also guards against in-flight duplicates (`queued`/`running`) for that `dedupe_key`.

### 3.5 Dispatch

**Bounded Worker Pool.** Ductile uses a global worker pool to process jobs in parallel. This ensures high throughput while preventing resource exhaustion.

- **Global Limit:** Controlled by `service.max_workers` (defaults to `max(1, CPU-1)`). Operators can force whole-system serial dispatch by setting `service.max_workers: 1`.
- **Plugin Parallelism:** Each plugin can define a `parallelism` limit in its configuration. The plugin manifest's `concurrency_safe` hint is the plugin author's declaration about whether same-plugin concurrent execution is safe; omitted means `true`.
- **Smart Dequeue:** The scheduler and dispatcher skip jobs for plugins that have reached their active parallelism cap, ensuring the worker pool remains available for other tasks. Running counts and same-`dedupe_key` execution exclusion are derived from `job_queue`; dispatcher in-memory counters are local worker lifecycle coordination only.

Revisit condition: sustained queue wait times exceed 60 seconds with all workers saturated.

### 3.6 Deduplication

When a producer enqueues a job with a `dedupe_key`:

1. Determine effective dedupe TTL: per-enqueue override (if provided), otherwise service `dedupe_ttl`.
2. If a per-enqueue override is set, query for an existing `queued` or `running` job with the same `dedupe_key`.
3. Query for a `succeeded` job with the same `dedupe_key` completed within the effective TTL.
4. If either check finds a match: do not enqueue. Log at `INFO`: dedupe_key, existing job ID.
5. If no match is found: enqueue normally.

During dispatch, a queued job with a `dedupe_key` is skipped while another job with the same `dedupe_key` **and the same target (`plugin` + `command`)** is `running`. The guard is per-target by design: a single source event that fans out to multiple distinct targets inherits one `dedupe_key`, and those distinct-target siblings must still run concurrently rather than serialise (and starve) behind each other. That execution serialisation is query-backed by `job_queue`, not a separate durable state table.

---

## 4. Scheduler

A single internal tick loop manages scheduled `poll` jobs. Each enabled plugin can define one or more schedule entries under `schedules:`. Plugins without schedules are ignored by the scheduler and can still be triggered via webhook, router, CLI, or API.

For a full field-by-field reference and behavior details, see [SCHEDULER.md](SCHEDULER.md).

### 4.1 Schedule Entries

Each schedule entry is independent and has its own ID (default: `default`), command, and payload:

```yaml
plugins:
  withings:
    schedules:
      - id: hourly
        every: 1h
        command: poll
        payload:
          source: heartbeat
```

Supported schedule types:
- `every`: Interval schedule (`5m`, `15m`, `30m`, `hourly`, `2h`, `daily`, `weekly`, `monthly`).
- `cron`: Standard 5-field cron (`min hour dom month dow`).
- `at`: One-shot RFC3339 timestamp.
- `after`: One-shot delay from service start.

### 4.2 Time Controls

Schedule execution can be constrained with time settings:
- `jitter`: Random offset per scheduled run.
- `only_between`: Time window string (e.g. `"08:00-22:00"`).
- `timezone`: IANA timezone for cron/window evaluation.
- `not_on`: List of weekdays to skip (`[saturday, sunday]` or `[0-6]`).

`preferred_window` exists in config but is not enforced yet.

Jitter is computed per scheduled run (not per tick):
```
next_run = last_successful_run + interval + random(-jitter/2, +jitter/2)
```

### 4.3 Catch-up and Overlap

Two per-schedule policies control missed ticks and concurrency:
- `catch_up`: `skip` (default), `run_once`, `run_all`.
- `if_running`: `skip` (default), `queue`, `cancel`.

### 4.4 Poll Guard

The scheduler **must not enqueue** a new `poll` job if there is already a `queued` or `running` `poll` job for that plugin. Configurable per-plugin (default 1):

```yaml
plugins:
  withings:
    max_outstanding_polls: 1
```

---

## 5. Plugin System

### 5.1 Lifecycle: Spawn-Per-Command

One process per job. No long-lived plugin processes.

1. Fork the plugin entrypoint.
2. Write JSON request to stdin.
3. Close stdin.
4. Read stdout until EOF or timeout.
5. Capture stderr.
6. Collect exit code.
7. Kill the process if it hasn't exited.

Process spawn overhead is ~5ms on Linux — irrelevant when the shortest interval is 5 minutes.

**Persistent connections (WebSockets, long-polling) are out of scope.** If needed, run as a separate service that pushes events into Ductile via the webhook endpoint. No streaming plugin mode — not now, not ever for this core.

### 5.2 Commands

| Command | Purpose | When |
|---------|---------|------|
| `poll` | Fetch data from external source | Scheduled by heartbeat |
| `handle` | Process an inbound event | Routed from another plugin or webhook |
| `health` | Diagnostic check | On-demand via `ductile status` |
| `init` | One-time setup | On first discovery or config change |

- `init` is not retried on failure — plugin is marked unhealthy.
- `health` is not called on a schedule — it's a diagnostic tool for the operator.

### 5.3 Plugin Directory Structure

```
plugins/
├── withings/
│   ├── manifest.yaml
│   └── run.py
├── google-calendar/
│   ├── manifest.yaml
│   └── run.py
├── notify/
│   ├── manifest.yaml
│   └── run.sh
└── lib/            # shared helpers (e.g. OAuth utilities)
```

### 5.4 Manifest

**Object format:**
```yaml
manifest_spec: ductile.plugin
manifest_version: 1
name: withings
version: 1.0.0
protocol: 2
entrypoint: run.py
description: "Fetch health data from Withings API"
commands:
  poll:
    type: read
    description: "Fetch latest measurements from Withings API"
  sync:
    type: write
    description: "Push weight data to Withings API"
  oauth_callback:
    type: write
    description: "Handle OAuth2 callback and store tokens"
  health:
    type: read
    description: "Health check"
config_keys:
  required: [client_id, client_secret]
  optional: [access_token]
```

**Command type semantics:**
- `type: read` - No external side effects, idempotent (safe for automated retries)
  - Examples: poll, fetch, get, list, health
  - May emit a durable snapshot via `state_updates` (declared as a `fact_outputs` rule for append-only persistence; the compatibility view is updated automatically).
  - Cannot POST/PUT/DELETE to external APIs
- `type: write` - Modifies external state, may not be idempotent
  - Examples: sync, send, notify, oauth_callback, delete
  - Default if type not specified (paranoid default)

**Purpose:** Enables manifest-driven token scopes (`plugin:ro` vs `plugin:rw`) without hardcoding command knowledge in auth middleware.

**Validation:**
- `manifest_spec` — must be `ductile.plugin`.
- `manifest_version` — must be `1`.
- `protocol` — must match a version the core supports. Mismatch → plugin not loaded.
- `entrypoint` — mandatory. Core constructs execution path relative to the discovered plugin directory.
- `config_keys.required` — validated at load time. Missing keys → plugin not loaded, error logged.
- `commands.*.type` — must be `read` or `write` if specified. Invalid type → plugin not loaded.

See card #36 (Manifest Command Type Metadata).

### 5.5 Trust & Execution

- Plugins MUST live under one of the configured plugin roots. Symlinks resolved, must resolve within an approved root.
- `..` in `entrypoint` is rejected (path traversal prevention).
- Entrypoint MUST be executable (`chmod +x`). Shebang line handles interpreter selection.
- World-writable plugin directories are refused at load time.
- Plugins run as the same OS user as the core. Use systemd `User=ductile` to limit blast radius.

### 5.6 Timeouts

**Defaults:**

| Command | Timeout |
|---------|---------|
| `poll` | 60s |
| `handle` | 120s |
| `health` | 10s |
| `init` | 30s |

**Enforcement:**

1. Core starts a deadline timer when spawning the process.
2. On timeout: `SIGTERM` to the process group.
3. 5-second grace period.
4. `SIGKILL` if still alive.
5. Job status → `timed_out`, follows retry policy.

**Configurable per-plugin:**

```yaml
plugins:
  slow-plugin:
    timeouts:
      poll: 300s
      handle: 300s
```

**Resource caps:**
- Max stdout: 10 MiB captured. Exceeding this cap is a protocol/output failure; the captured prefix is kept for diagnostics.
- Max stderr: 64 KiB captured for diagnostics. Excess stderr is truncated with a logged warning.

### 5.7 Retry & Backoff

- Default: 4 attempts total (1 original + 3 retries).
- Backoff: `base * 2^(attempt-1) + random(0, base)` where `base = 30s`.
- Retry delays: ~30s, ~1m, ~2m (then dead).

**Non-retryable conditions:**
- Plugin exits with code `78` (EX_CONFIG from sysexits.h) — configuration error.
- Plugin response may include `"retry": false`; core treats this as a compatibility signal, not plugin-owned policy.
- All other failures are retried.

**Configurable per-plugin:**

```yaml
plugins:
  withings:
    retry:
      max_attempts: 5
      backoff_base: 60s
```

### 5.8 Circuit Breaker

Configurable consecutive failure threshold per `(plugin, command)` pair. Applies to **scheduler-originated poll jobs only** — webhook-triggered `handle` jobs are not blocked by poll failures.

- Default threshold: 3 consecutive failures.
- Default reset: 30 minutes.
- Manual reset: `ductile system reset <plugin>`.
- Inspect state and transition history: `ductile system breaker <plugin> [--json]`.
- States: `closed` -> `open` -> `half_open`.
- When cooldown expires, scheduler allows a single half-open probe poll:
  - Success closes the circuit and resets failure count.
  - Failure reopens the circuit.

```yaml
plugins:
  withings:
    circuit_breaker:
      threshold: 3
      reset_after: 30m
```

### 5.9 State Model

**Config is static. Facts are durable. `plugin_state` is a compatibility view.**

- `config` — from `config.yaml`, interpolated with env vars, read-only. Contains credentials, endpoints — things the operator sets.
  - Config paths (config dir, includes, backups) are local operator-controlled inputs; Ductile does not accept untrusted remote file paths.
  - `service.allow_symlinks` controls whether symlinks are permitted in config/plugin paths (warnings are always emitted when symlinks are detected).
- `plugin_facts` — append-only record of durable plugin observations. Each row carries a stable snapshot the plugin emitted as `state_updates`, plus a `fact_type` declared in the plugin manifest's `fact_outputs` and a Ductile-owned monotonic `seq`. This is the durable record. See [PLUGIN_FACTS.md](PLUGIN_FACTS.md).
- `plugin_state` — single JSON row per plugin maintained as a compatibility/cache view of the latest fact. Existing readers see the same shape they saw before facts existed. The view is rebuilt automatically by core when a fact lands, governed by the manifest's `compatibility_view` declaration (currently `mirror_object`). Plugins that have not declared `fact_outputs` still get write-through behaviour during the compatibility window; new plugins should declare `fact_outputs` rather than treating this row as their durable home.

```sql
-- Append-only durable record (primary).
plugin_facts (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  seq         INTEGER NOT NULL,         -- Ductile-owned monotonic
  plugin_name TEXT NOT NULL,
  fact_type   TEXT NOT NULL,
  job_id      TEXT,
  command     TEXT,
  fact_json   JSON NOT NULL,
  created_at  TEXT NOT NULL
);

-- Compatibility/cache view of the latest fact (derived).
plugin_state (
  plugin_name TEXT PRIMARY KEY,
  state       JSON NOT NULL DEFAULT '{}',
  updated_at  TIMESTAMP
);
```

**Size limit:** 1 MB per `plugin_state` row. Exceeding this rejects the update and fails the job. The same limit constrains the snapshot a plugin emits, since the compatibility view mirrors it.

### 5.10 OAuth

Plugins manage their own OAuth token lifecycle. The core does not understand OAuth.

- `client_id`, `client_secret` → `config` (static, set by operator).
- `access_token`, `refresh_token`, `token_expiry` → managed by the plugin and emitted as part of its `state_updates` snapshot. The plugin should declare a `fact_outputs` rule so each token-refresh observation is recorded append-only and the compatibility view stays current for downstream readers.
- Plugin checks expiry on each invocation, refreshes if needed, returns new tokens via `state_updates`.
- Shared OAuth helpers can live in `plugins/lib/`.

---

## 6. Protocol (v2)

### 6.1 Request Envelope (core → plugin)

Single JSON object written to plugin's stdin:

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

- `event` — present only for `handle`.
- `state` — the plugin's current compatibility-view row (the latest fact's snapshot, or write-through state for plugins not yet declaring `fact_outputs`).
- `context` — shared metadata (Baggage) carried across the pipeline chain.
- `deadline_at` — informational. Plugins MAY use it to abandon long-running work early. The core enforces the real deadline externally.

### 6.2 Response Envelope (plugin → core)

Single JSON object written to plugin's stdout:

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

- `result` — required when `status=ok`. Summarizes what the plugin did.
- `retry` — response-envelope compatibility signal. Defaults to `true` if omitted. Set `false` for permanent failures; core still owns the retry decision with exit status, attempts, and config as inputs.
- `events` — array of event envelopes (see 6.3).
- `state_updates` — the plugin's emitted snapshot. When the manifest declares a matching `fact_outputs` rule, core records this snapshot as an append-only `plugin_facts` row and rebuilds the compatibility view from it. Plugins without a declared `fact_outputs` rule get write-through into `plugin_state` directly during the compatibility window.
- `logs` — array of `{"level": "info|warn|error", "message": "..."}`. Optional. Stored with the job record.

### 6.3 Event Envelope

Every event emitted by a plugin in the `events` array:

```json
{
  "type": "new_health_data",
  "payload": {},
  "dedupe_key": "withings:weight:2026-02-08"
}
```

- `type` — matches `event_type` in routing config. Exact string match.
- `payload` — arbitrary JSON, passed to downstream plugin's `handle` command.
- `dedupe_key` — optional. Downstream job inherits this as its `dedupe_key`.

The core injects when creating downstream jobs:
- `source` — plugin name.
- `timestamp` — ISO8601.
- `event_id` — UUID assigned by the core.

### 6.4 Framing

Single JSON object on stdout. Not JSON Lines, not length-prefixed. One request, one response, process exits.

### 6.5 Protocol Mismatch

If the request `protocol` field doesn't match what the plugin expects, the plugin SHOULD exit with code `78` (EX_CONFIG) and a clear error on stderr. The core refuses to load plugins whose manifest declares a `protocol` version it doesn't support.

---

## 7. Routing

Plugin chaining is declared in config, not by plugins. Plugins produce typed events; the config says where they go.

### 7.1 Config

```yaml
routes:
  - from: withings
    event_type: new_health_data
    to: health-analyzer

  - from: health-analyzer
    event_type: alert
    to: notify
```

### 7.2 Semantics

- **Fan-out:** A single event can match multiple routes. All matching routes produce a job.
- **No match:** Logged at DEBUG, dropped. Not an error.
- **Matching:** Exact string match on `event_type` only. No wildcards, no regexes, no glob patterns.
- **No conditional filters.** No `payload.severity == "high"`. If you need conditional logic, put it in the receiving plugin — it can inspect the payload and no-op.

### 7.3 Traceability

When the router creates a downstream job from an event:
- `parent_job_id` is set to the producing job's ID.
- `source_event_id` is set to the core-assigned `event_id`.

---

## 8. Pipelines (DSL)

Pipelines provide a higher-level orchestration layer over raw routes, using a GitHub Actions-inspired notation.

### 8.1 Schema

```yaml
pipelines:
  - name: youtube-summary
    on: discord.command.youtube    # Trigger event type
    execution_mode: synchronous     # Optional: async | synchronous
    timeout: 3m                    # Optional: duration (default 30s)
    steps:
      - id: download               # Optional
        uses: youtube.download     # plugin.command
        
      - id: summarize
        uses: fabric.summarize
        
      - id: notify
        uses: discord.respond
```

### 8.2 Execution Modes

- **async (default):** Fire-and-forget. The API returns `202 Accepted` with a `job_id` immediately. Dispatcher handles jobs as they come.
- **synchronous (opt-in):** The API caller "stays on the line". The gateway waits for the entire execution tree (all steps) to reach a terminal state before responding with aggregated results.

### 8.3 Guarded Bridge

The engine remains event-driven and asynchronous internally. Synchronous behavior is implemented as a "Guarded Bridge" at the API layer:
1. Dispatcher provides completion channels for job trees.
2. API handler blocks on these channels.
3. If `timeout` is exceeded, the bridge "breaks" and returns `202 Accepted` with the root `job_id`, allowing the client to poll for completion.

---

## 9. API Endpoints

The HTTP API allows external systems (LLMs, scripts, other services) to programmatically trigger plugin execution and retrieve job results.

### 9.1 Configuration

```yaml
api:
  enabled: true
  listen: "localhost:8080"
  auth:
    tokens:
      - token: ${ADMIN_API_TOKEN}
        scopes: ["*"]
```

### 9.2 Primary Trigger Endpoints

The API exposes two first-class trigger paths:

- `POST /plugin/{plugin}/{command}`: direct plugin execution (no pipeline routing), returns `202 Accepted`.
- `POST /pipeline/{pipeline}`: explicit pipeline orchestration, returns `202 Accepted` by default and `200 OK` for synchronous pipelines.

See `docs/API_REFERENCE.md` for full examples and response schemas.

### 9.3 GET /job/{job_id}

Retrieves the status and results of a previously triggered job.

**Request:**
- URL param: `{job_id}` - UUID returned from one of the POST trigger endpoints
- Header: `Authorization: Bearer <token>`

**Response (200 OK - queued):**
```json
{
  "job_id": "uuid-v4",
  "status": "queued",
  "plugin": "plugin_name",
  "command": "command_name",
  "created_at": "2026-02-09T10:00:00Z"
}
```

**Response (200 OK - running):**
```json
{
  "job_id": "uuid-v4",
  "status": "running",
  "plugin": "plugin_name",
  "command": "command_name",
  "started_at": "2026-02-09T10:00:05Z"
}
```

**Response (200 OK - completed):**
```json
{
  "job_id": "uuid-v4",
  "status": "completed",
  "plugin": "plugin_name",
  "command": "command_name",
  "result": {
    "status": "ok",
    "result": "Plugin executed successfully",
    "state_updates": {"last_run": "2026-02-09T10:00:10Z"},
    "logs": [{"level": "info", "message": "Plugin executed successfully"}]
  },
  "started_at": "2026-02-09T10:00:05Z",
  "completed_at": "2026-02-09T10:00:10Z"
}
```

**Error Responses:**
- `401 Unauthorized` - Missing or invalid token
- `404 Not Found` - Job ID not found

### 9.4 Authentication & Authorization

**Bearer token authentication** with scoped permissions.

**Token registry** (`tokens.yaml`):
- Multiple tokens with individual scope definitions
- Each token references a scope file (JSON)
- BLAKE3 hash ensures scope file integrity
- Environment variable references for keys (never plaintext)

**Scope types (current):**
- `plugin:ro`, `plugin:rw` - Plugin and pipeline trigger permissions
- `jobs:ro`, `jobs:rw` - Job read/write permissions
- `events:ro`, `events:rw` - Event stream permissions
- `*` - Full admin access

**Example tokens.yaml:**
```yaml
tokens:
  - name: admin-cli
    key: ${ADMIN_API_KEY}
    scopes_file: scopes/admin-cli.json
    scopes_hash: blake3:a3f8c2d9...

  - name: github-integration
    key: ${GITHUB_API_KEY}
    scopes_file: scopes/github-integration.json
    scopes_hash: blake3:b4e9d3c0...
```

**Example scope file (scopes/github-integration.json):**
```json
{
  "scopes": [
    "read:jobs",
    "read:events",
    "github-handler:rw",
    "withings:ro"
  ]
}
```

**Authorization middleware:**
1. Extract bearer token from `Authorization` header
2. Lookup token in registry
3. Load and verify scope file (BLAKE3 hash check)
4. Normalize implied read-from-write scopes
5. Check if requested action matches any granted scope
6. Return 403 if denied, proceed if allowed

Tokens should be stored in environment variables and interpolated (for example `${ADMIN_API_TOKEN}`).

- All API requests must include `Authorization: Bearer <token>` header
- Invalid or missing token returns `401 Unauthorized`
- No key rotation mechanism in MVP (manual config update + reload)

### 9.5 Resource Guarding (Synchronous Pipelines)

To prevent HTTP worker exhaustion, synchronous pipelines are governed by a semaphore:
- **api.max_concurrent_sync:** Max number of simultaneous blocking API calls (default 10).
- **api.max_sync_timeout:** Hard limit on pipeline timeout to prevent zombie connections.

### 9.6 Use Cases

- **LLM Tool Calling:** LLM agents can call `/plugin` for atomic actions and `/pipeline` for orchestrated workflows
- **External Automation:** Scripts, cron jobs, or other services can trigger plugins programmatically
- **Result Polling:** External systems can poll /job/{id} to wait for async plugin execution completion
- **Manual Testing:** Developers can trigger plugins via curl without waiting for scheduler

---

## 10. Webhooks

For operator setup and example requests, see [WEBHOOKS.md](WEBHOOKS.md).

### 10.1 Listener

```yaml
webhooks:
  listen: 127.0.0.1:8081
  endpoints:
    - path: /hook/github
      plugin: github-handler
      secret_ref: github_webhook_secret
      signature_header: X-Hub-Signature-256
      max_body_size: 1MB
```

### 10.2 Security

HMAC-SHA256 signature verification is **mandatory** for all webhook endpoints.

1. Read raw request body (up to `max_body_size`, default 1 MB).
2. Resolve `secret_ref` from tokens.yaml and compute `HMAC-SHA256(secret, raw_body)`.
3. Compare against the signature header (configurable name per endpoint).
4. Reject with `403` if invalid. No error details in response.
5. Reject with `413` if body exceeds `max_body_size`.

No replay protection in V1. No rate limiting in V1 (proxy responsibility if fronted by reverse proxy).

### 10.3 Health Endpoint

`/healthz` on the webhook listener port:

```json
{
  "status": "ok",
  "uptime_seconds": 3600,
  "queue_depth": 2,
  "plugins_loaded": 5,
  "plugins_circuit_open": 0
}
```

No authentication. Localhost only. Useful for systemd watchdog and operator checks.

---

## 11. Operations

### 11.1 Single-Instance Lock

PID file with `flock(LOCK_EX | LOCK_NB)`:

1. Create/open `<state_dir>/ductile.lock`.
2. Acquire `flock`. Fail → log error, exit 1.
3. Write current PID.
4. Lock held for process lifetime. Kernel releases on crash/exit.

### 11.2 Crash Recovery

On startup:

1. Open the SQLite database.
2. Acquire the exclusive lock.
3. Find all jobs with `status = running` — orphans from a prior crash.
4. For each orphan: increment `attempt`, set `status = queued` if under `max_attempts`, else `status = dead`.
5. Log each recovered job at WARN level.
6. Resume normal dispatch.

### 11.3 Config Reload

Send `SIGHUP` to the running process (found via PID file) to reload config.

On SIGHUP:

1. Parse new config. If invalid → log error, keep old config.
2. In-flight jobs continue with existing config snapshot.
3. Scheduler updates intervals/jitter for all plugins.
4. Router updates routing rules.
5. Plugin config changes take effect on next dispatch.
6. Newly added plugins discovered → `init` runs.
7. Removed/disabled plugins → queued jobs cancelled (status → `dead`), no new jobs enqueued.

### 11.4 Logging

**Core logs:** JSON to stdout.

Fields: `timestamp`, `level`, `component`, `plugin` (when relevant), `job_id` (when relevant), `message`.

**Plugin stderr:** Captured. Always. Stored in `job_log` (capped at 64 KB). Logged at WARN to core log stream.

**Plugin stdout:** Reserved exclusively for protocol response. Stored verbatim on completion in `job_log.result` (JSON). Non-JSON on stdout is a protocol error — job fails, stderr + stdout captured for debugging.

**Redaction:** Not in V1. Don't log secrets. Fix the plugin, don't bandage the core.

### 11.5 Job Log Retention

Pruned on every scheduler tick:

```sql
DELETE FROM job_log WHERE completed_at < datetime('now', '-30 days')
```

Default 30 days. Configurable via `service.job_log_retention`.

### 11.6 CLI

```
ductile system start       # run the service (foreground)
ductile run <plugin>       # manually run a plugin once
ductile status             # show plugin compatibility views, queue depth, last runs
# send SIGHUP to reload config without restart
ductile system reset <plugin>     # reset circuit breaker for a plugin
ductile plugins            # list discovered plugins
ductile logs [plugin]      # tail structured logs
ductile queue              # show pending/active jobs
```

### 11.7 CLI Principles

To ensure predictability and safety for both human and LLM operators, all CLI commands MUST adhere to the standards defined in `docs/CLI_DESIGN_PRINCIPLES.md`.

Core requirements:
- **Hierarchy:** Strict **NOUN ACTION** pattern.
- **Verbosity:** mandatory `-v` / `--verbose` flags.
- **Safety:** mandatory `--dry-run` for mutations.
- **Machine-Readability:** mandatory `--json` for status and inspection.

---

## 12. Database Schema

Ductile uses a persistent SQLite database for the work queue, append-only logs, metrics telemetry, and state store. 

### 12.1 Tables

```sql
-- Job queue (active and pending jobs)
job_queue (
  id              TEXT PRIMARY KEY,   -- UUID
  plugin          TEXT NOT NULL,
  command         TEXT NOT NULL,       -- poll | handle
  payload         JSON,
  status          TEXT NOT NULL,       -- queued | running | succeeded | failed | timed_out | dead
  attempt         INTEGER NOT NULL DEFAULT 1,
  max_attempts    INTEGER NOT NULL DEFAULT 4,
  submitted_by    TEXT NOT NULL,       -- scheduler | webhook | route | cli
  dedupe_key      TEXT,
  created_at      TEXT NOT NULL,       -- ISO8601
  started_at      TEXT,
  completed_at    TEXT,
  next_retry_at   TEXT,
  last_error      TEXT,
  parent_job_id   TEXT,                -- FK to job_queue.id
  source_event_id TEXT,                -- UUID assigned by core
  event_context_id TEXT,               -- ID of associated baggage row
  enqueued_config_snapshot_id TEXT,     -- Config snapshot active at enqueue
  started_config_snapshot_id TEXT       -- Config snapshot active at start
);

-- Append-only job status transitions history
job_transitions (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id      TEXT NOT NULL,           -- Joined to job_queue.id (no FK to allow pruning)
  from_status TEXT,
  to_status   TEXT NOT NULL,
  reason      TEXT,
  created_at  TEXT NOT NULL            -- ISO8601
);

-- Append-only job execution attempt log
job_attempts (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id     TEXT NOT NULL,            -- Joined to job_queue.id (no FK to allow pruning)
  attempt    INTEGER NOT NULL,
  created_at TEXT NOT NULL,            -- ISO8601
  UNIQUE(job_id, attempt)
);

-- Config snapshots (durable record of active configurations)
config_snapshots (
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

-- Append-only durable plugin record (primary).
plugin_facts (
  id          TEXT PRIMARY KEY,
  seq         INTEGER,                  -- Ductile-owned monotonic sequence
  plugin_name TEXT NOT NULL,
  fact_type   TEXT NOT NULL,            -- e.g. "<plugin>.snapshot"
  job_id      TEXT NOT NULL,
  command     TEXT NOT NULL,
  fact_json   JSON NOT NULL,
  created_at  TEXT NOT NULL
);

-- Compatibility/cache view of the latest fact (derived).
-- One row per plugin. Existing readers see the same shape as before facts existed.
plugin_state (
  plugin_name TEXT PRIMARY KEY,
  state       JSON NOT NULL DEFAULT '{}',
  updated_at  TEXT
);

-- Job log (completed jobs for audit/debugging)
job_log (
  id              TEXT PRIMARY KEY,
  plugin          TEXT NOT NULL,
  command         TEXT NOT NULL,
  status          TEXT NOT NULL,
  result          TEXT,                -- protocol response JSON
  attempt         INTEGER NOT NULL,
  submitted_by    TEXT NOT NULL,
  created_at      TEXT NOT NULL,
  completed_at    TEXT NOT NULL,
  last_error      TEXT,
  stderr          TEXT,                -- capped at 64 KB
  parent_job_id   TEXT,
  source_event_id TEXT
);

-- Circuit breaker state for scheduler poll guard
circuit_breakers (
  plugin          TEXT NOT NULL,
  command         TEXT NOT NULL,       -- poll
  state           TEXT NOT NULL,       -- closed | open | half_open
  failure_count   INTEGER NOT NULL DEFAULT 0,
  opened_at       TEXT,                -- ISO8601
  last_failure_at TEXT,                -- ISO8601
  last_job_id     TEXT,                -- latest processed scheduler poll job id
  updated_at      TEXT NOT NULL,       -- ISO8601
  PRIMARY KEY(plugin, command)
);

-- Append-only circuit breaker transition facts.
-- circuit_breakers remains the current-state compatibility/cache row.
circuit_breaker_transitions (
  id            TEXT PRIMARY KEY,
  plugin        TEXT NOT NULL,
  command       TEXT NOT NULL,
  from_state    TEXT,                -- closed | open | half_open | NULL
  to_state      TEXT NOT NULL,       -- closed | open | half_open
  failure_count INTEGER NOT NULL DEFAULT 0,
  reason        TEXT NOT NULL,       -- failure_threshold | success | cooldown_elapsed | manual_reset
  job_id        TEXT,
  created_at    TEXT NOT NULL        -- ISO8601
);
```

---

## 13. Configuration Reference

Ductile uses a **Monolithic Runtime** compiled from a modular, **Tiered Directory** structure.

### 13.1 Overview

For the complete configuration specification, including file formats, merge logic, and integrity verification rules, see:  
👉 **[docs/CONFIG_REFERENCE.md](CONFIG_REFERENCE.md)**

### 13.2 Key Principles

- **Include-Based Modularity:** Configuration is loaded from `config.yaml` plus any files or directories listed in `include:`.
- **Multi-Root Plugin Discovery:** `plugin_roots` is the source of truth; roots are scanned in order and first match wins on duplicate plugin names.
- **Pipeline Discovery Flow:** Pipelines are loaded from included YAML files (or include directories) that define `pipelines:` entries.
- **Tiered Integrity:** High-security files (auth/webhooks) require a valid BLAKE3 hash in `.checksums` to start. Operational files (settings/routes) log warnings if hashes are missing or mismatched.
- **Monolithic Grafting:** At runtime, all included files are merged into a single internal configuration object following strict precedence rules (later entries override earlier ones).
- **Environment Interpolation:** Secrets are injected via `${VAR}` placeholders, which are interpolated after hash verification but before parsing.
- **Default Permissions:** Config directories are created with `0700`. Config files and lock files default to `0600`; operators may relax permissions explicitly for shared environments.
- **Secret Redaction:** CLI config inspection outputs redact token keys and webhook secrets; secrets are only shown at creation time.

## 14. Deployment

### 14.1 Systemd Unit

```ini
[Unit]
Description=Ductile
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/ductile system start --config /etc/ductile/config.yaml
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
User=ductile
Group=ductile

[Install]
WantedBy=multi-user.target
```

### 14.2 Development

Run `ductile system start` directly. No systemd required.

---

## 15. Project Layout

```
ductile/
├── cmd/
│   └── ductile/
│       └── main.go
├── internal/
│   ├── config/
│   ├── queue/
│   ├── scheduler/
│   ├── dispatch/
│   ├── plugin/
│   ├── state/
│   ├── api/
│   ├── webhook/
│   └── router/
├── plugins/
│   └── example/
│       ├── manifest.yaml
│       └── run.py
├── config.yaml
├── go.mod
├── go.sum
└── Makefile
```

---

## 16. Implementation Phases

| Phase | Sprint | Scope | Status |
|-------|--------|-------|--------|
| 1. Skeleton | 0 | Go scaffold, CLI, config loader, SQLite state, plugin discovery | ✅ Complete |
| 2. Core Loop | 1 | Work queue, heartbeat scheduler with fuzzy intervals, dispatch loop, plugin protocol, crash recovery | ✅ Complete |
| 3. API Triggers | 2 | HTTP server with chi router, POST /plugin and POST /pipeline, GET /job, Bearer token auth, job result storage | ✅ Complete |
| 4. Routing | 3 | Config-declared event routing, downstream enqueuing, event_id traceability | ✅ Complete |
| 5. Webhooks | 3 | HTTP listener, HMAC verification, /healthz, route inbound webhooks to plugins | ✅ Complete |
| 6. Reliability Controls | 4 | Circuit breaker, retry with exponential backoff, deduplication enforcement | ✅ Complete |
| 7. Pipeline Orchestration | 4 | Sync/Async execution modes, Guarded Bridge, YAML DSL, completion channels | ✅ Complete |
| 8. CLI & Ops | 5 | Status/run/reload/reset/plugins/queue/logs commands, systemd unit | 🔄 In Progress (Status: ✅ Status implemented) |
| 9. First Plugins | 6 | Port Withings & Garmin from existing Ductile, notify plugin | Planned |

**Note:** Phase 3 (API Triggers) was prioritized before Routing and Webhooks to enable LLM-driven automation via curl-based triggers. This allows external systems to programmatically enqueue jobs and retrieve results immediately, accelerating the path to production use cases.

---

## 17. Deferred Decisions

| Topic | Rationale |
|-------|-----------|
| Two-tier stderr/stdout caps (capture vs persistence) | Current spec is workable. Clarify post-V1 if storage becomes a concern. |
| `protocol` field in response envelope | Accretive addition; back-compatible with plugins that omit it. |
| Replay protection for webhooks | Provider-specific. Add per-plugin if a provider requires it. |
| Rate limiting on webhook listener | Proxy responsibility. Core doesn't duplicate concerns it can't own. |
| Secret redaction in logs | Operator responsibility. Fix the plugin, don't bandage the core. |
| Streaming / long-lived plugin mode | Out of scope permanently. If it needs to stream, it's not a plugin. |
| Priority queues / multi-lane dispatch | Revisit only if daily jobs exceed 500 or median wait exceeds 30s. |
| Router query language / payload filters | Put conditional logic in the receiving plugin. |
