# Ductile Glossary

Key terms used throughout Ductile's documentation and configuration.

---

## Gateway

The `ductile` binary — the central runtime that manages plugins, schedules work, routes events, and maintains the execution ledger.

## Plugin / Connector

A polyglot adapter that connects Ductile to an external system (an API, a database, a shell command). Written in any language; communicates via JSON over stdin/stdout. 
- **Plugin:** The code and manifest (the implementation).
- **Connector:** The logical integration point (the "skill").

## Alias (Plugin Instance)

A uniquely named and configured instance of a base plugin. Defined in `plugins.yaml` using the `uses:` field. Allows running multiple copies of the same logic (e.g., `discord_alerts` vs `discord_logs`) with different settings.

## Command

A discrete operation provided by a plugin. Common commands include:

- **`poll`** — proactive; Ductile calls the plugin on a schedule to pull data.
- **`handle`** — reactive; the plugin processes an incoming event.
- **`health`** — diagnostic; verifies the plugin's prerequisites are met.
- **`init`** — one-time setup; runs when a plugin is first registered.

## Pipeline

A high-level workflow orchestration defined in YAML. Pipelines react to a single trigger event and execute a sequence of plugin steps, automatically passing data between them.

## Event Router (Pipeline & Event Routing)

The internal deterministic routing layer (`internal/router`) that maps events emitted by plugins, webhook receivers, or the API to downstream actions. Rather than running as an asynchronous in-memory pub/sub "event bus," the Event Router evaluates incoming events and context triggers against the configured pipelines and global routes, producing new job dispatches that are transactionally enqueued back into the SQLite Work Queue. This keeps the work queue as the single source of truth and durability boundary for all execution states.

## Event Hub

An in-memory pub/sub ring-buffer (`internal/events`) used exclusively for passive diagnostics, logging, and observation (such as real-time updates for the TUI or event stream API). Unlike the Event Router, the Event Hub is completely decoupled from the execution path to ensure slow observers never block core workers.

## Event

A typed packet of data (e.g., `youtube.playlist_item`) that signals an occurrence and triggers routing within the gateway.

## Payload

The JSON object attached to an event. Payload fields are passed to downstream plugins when the event is routed.

## Context (Baggage)

Immutable metadata (e.g., `origin_user_id`, `trace_id`) that persists across every hop of a multi-step pipeline once a step claims it with `baggage`. Carried in the `event_context` ledger and merged into downstream requests.

## Worker Pool (Max Workers)

The global set of execution slots that process jobs in parallel. Controlled by `service.max_workers` (defaults to `max(1, CPU-1)`). Operators can force whole-system serial dispatch by setting it to `1`.

## Worker / Consumer

An individual execution slot within the bounded worker pool. Workers continuously pull (dequeue) eligible jobs from the SQLite Work Queue and execute them by spawning plugin subprocesses.

## Parallelism

The maximum number of concurrent jobs allowed for a specific plugin or alias. Prevents a single resource-heavy plugin from saturating the worker pool.

## Concurrency Safe

A boolean hint in a plugin's `manifest.yaml`. Omitted means `true`. If set to `false`, the plugin author is declaring that same-plugin concurrent execution is unsafe unless an operator deliberately constrains or overrides plugin parallelism.

## Smart Dequeue

The logic that skips jobs in the queue if their target plugin has already reached its parallelism limit, allowing other plugins to proceed.

## Result

The human-readable summary or data returned by a plugin in its protocol response. Often used as the input for the next step in a pipeline.

## Plugin Facts

The append-only record of durable plugin observations. Each row carries a
stable snapshot a plugin emitted as `state_updates`, plus a manifest-declared
`fact_type` and a Ductile-owned monotonic `seq`. This is the durable record
of what a plugin remembers across runs. See [PLUGIN_FACTS.md](./PLUGIN_FACTS.md).

## Plugin State (Compatibility View)

A single JSON row per plugin maintained as the compatibility/cache view of
the latest fact. Existing readers (and legacy plugins that have not yet
declared `fact_outputs`) see the same shape they always have. The view is
rebuilt automatically by core when a new fact lands. New plugins should
declare `fact_outputs` rather than treating this row as the place where
durable truth lives.

## Producer

Any system component that enqueues a job into the Work Queue. Primary producers are: the Scheduler (heartbeat ticks), Webhook Receiver (inbound HTTP payloads), Event Router (routing pipeline steps), and the CLI or API.

## Job

The atomic unit of work in Ductile. Every command invocation creates an immutable Job record capturing input, output, logs, and status.

## Queue (SQLite-backed Work Queue)

The persistent, SQLite-backed First-In, First-Out (FIFO) work queue that acts as the transactional durability boundary for Ductile. All job executions, retries, and step transitions pass through this queue to survive crashes, timeouts, and system restarts.

## Enqueue

The action of transactionally appending a new job to the SQLite Work Queue.

## Dequeue

The action of a worker pulling the next eligible job from the SQLite Work Queue for execution.

## FIFO (First-In, First-Out)

The ordering principle of the SQLite Work Queue. Jobs are processed strictly in the order they are enqueued, subject to concurrency and parallelism caps.

## Schedule

A configuration entry that tells the scheduler when and how to run a plugin command (e.g., `every: 5m`, `cron: "0 * * * *"`).

## Jitter

A random offset applied to schedules to prevent multiple jobs from triggering at the exact same millisecond (the "thundering herd" problem).

## Deduplication (Dedupe Key)

The mechanism to prevent duplicate enqueues or redundant processing. If a job is submitted with a `dedupe_key`, enqueue is suppressed if a matching job is already in-flight (status `queued` or `running`) or succeeded within the deduplication window (`dedupe_ttl`).

## Circuit Breaker

An automated safety switch that "opens" after repeated plugin failures, temporarily blocking scheduled runs to allow the system or external API to recover.

## Webhook

An HMAC-verified HTTP endpoint that accepts external events and transactionally enqueues them into the Ductile SQLite Work Queue via the Event Router.

## Skill

A machine-readable description of a capability (either an atomic plugin command or an orchestrated pipeline), exported via `/skills` or `/openapi.json`.

## Workspace (historical)

Formerly: a per-job, hard-link-cloned directory the core provisioned
for each plugin invocation. Removed; the core no longer touches the
filesystem on a job's behalf. Plugins that need a scratch
path manage it themselves.

## Execution Ledger

The persistent history of all jobs, pipeline steps, and event transitions. Used for the TUI "Overwatch" and audit logging.
