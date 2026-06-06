# Ductile: Pipelines & Orchestration (DSL Reference)

Ductile uses a YAML-based Domain Specific Language (DSL) to define event-driven workflows. Pipelines transform atomic **Connectors** into complex, multi-hop **Orchestrations**.

---

## 1. Top-Level Structure

A pipeline file (e.g., `pipelines.yaml`) contains an array of pipeline definitions.

```yaml
pipelines:
  - name: my-workflow      # Required: Unique identifier
    on: my.event.type      # Required: Trigger event type
    execution_mode: async  # Optional: async (default) | synchronous
    timeout: 30s           # Optional: For synchronous execution
    steps:                 # Required: Sequential steps
      - uses: my-plugin
```

---

## 2. Pipeline Properties

| Field | Type | Description |
|-------|------|-------------|
| `name` | String | A unique name for the pipeline. Used for logging and API triggers. |
| `on` | String | The event type that triggers this pipeline. Must match exactly. |
| `on-hook` | String | Lifecycle signal that triggers this pipeline (`job.completed` / `job.failed` / `job.timed_out`). Mutually exclusive with `on`. |
| `from_plugin` | String | **Optional source-plugin selector.** When set, the trigger or hook signal only matches when the upstream source plugin is exactly this plugin. Empty (default) preserves today's behaviour — match regardless of source plugin. See §2.2. |
| `if` | Condition | **Optional pipeline-level trigger predicate.** Evaluated against the event's payload (and the upstream job's accumulated durable context, when available) after the trigger/hook name match; a false result skips dispatch entirely. Same shape as step-level `if:` (see §3.6). |
| `max_depth` | Integer | **Optional author-set route depth cap.** Overrides the auto-computed cap. `0` means *unlimited*. Negative values are rejected at config load. |
| `execution_mode`| Enum | `async` (fire-and-forget) or `synchronous` (API blocks for result). |
| `timeout` | Duration| Max time to wait for a `synchronous` pipeline (e.g., `5s`, `2m`). |
| `steps` | Array | The list of steps to execute in order. |

### 2.1 Pipeline-level `if:` vs. step-level `if:`

Both `if:` blocks share the same predicate engine — atomic
`path/op/value` plus `all/any/not`. They differ in *where* they evaluate:

| Surface | Evaluated when | Scope | Effect on false |
|---|---|---|---|
| Pipeline-level `if:` | Trigger/hook name has matched, before any dispatch | `payload`, `context` (when available) | No dispatch at all — no `core.switch`, no plugin spawn |
| Step-level `if:` (§3.6) | At each step, after upstream steps run | `payload`, `context`, `config` | Step bypassed via internal `core.switch`; downstream steps still run |

Use pipeline-level `if:` to **suppress dispatch** when an event isn't
relevant to a pipeline at all. Use step-level `if:` to **gate a step**
within a pipeline that is otherwise running.

#### Context availability at trigger time

`context.*` paths in a pipeline-level `if:` resolve against the upstream
job's *accumulated durable context* — the same baggage view that
downstream pipeline steps see. Context is available when the routed
event was emitted by a plugin running inside an existing pipeline; it
is empty for events from the scheduler tick, webhook ingress, and
direct API triggers. A predicate that tests `context.*` against an
absent context simply evaluates to false (no special-case error), the
same way a payload predicate against an absent key returns false.

For hook pipelines (`on-hook:`), context is **never** available at hook
fire time: hooks fire only for root jobs that have no upstream context
of their own. A `context.*` path in a hook pipeline's `if:` could
therefore never match, so it is **rejected at config load** with
`on-hook trigger predicate cannot reference context.* — context is not
available at hook time`. Scope hooks with `payload.*` predicates (and
`from_plugin:`, §2.2) instead.

A pipeline may use **both** in the same definition.

```yaml
- name: repo-changelog
  on: git_repo_sync.completed
  if:                              # pipeline-level: skip dispatch when no work
    path: payload.new_commits
    op: eq
    value: true
  steps:
    - id: changelog
      uses: changelog_microblog
    - id: commit
      uses: git_commit_push
      if:                          # step-level: only commit if the step before
        path: payload.changed       #             actually produced changes
        op: eq
        value: true
```

`max_depth` is a separate concern: it caps how many internal `core.switch`
hops a pipeline may chain before the runtime considers the route
exhausted. Author-setting it is rare; the auto-computed value is
correct in almost all cases. Set `max_depth: 0` only when you have a
deliberate need for unbounded recursion through `call:`, and you have
read §6.4 of this doc.

#### Hook-trigger predicate (`on-hook:` + `if:`)

Lifecycle hook pipelines (`on-hook: job.completed | job.failed | job.timed_out`)
fire for **every** matching lifecycle event across the whole runtime.
Without a predicate, this is fundamentally noisy. A pipeline-level `if:`
is the correct surface for scoping a hook pipeline:

```yaml
- name: notify-on-real-failure
  on-hook: job.failed
  if:
    not:
      path: payload.plugin
      op: in
      value: [check_youtube, jina-reader]   # known-noisy plugins
  steps:
    - uses: discord_notify
```

Hook predicates evaluate against the lifecycle event's payload, which
includes the plugin name, status, attempt count, and other lifecycle
fields documented in §9.

### 2.2 `from_plugin:` source-plugin selector

Pipelines that fire from plugin-emitted events (`on:`) or lifecycle
hooks (`on-hook:`) can be scoped to a single upstream plugin with the
optional `from_plugin:` field. When set, the route only matches when
the event's source plugin is exactly the named plugin.

```yaml
- name: page-on-claude-failure
  on-hook: job.failed
  from_plugin: claude_harvest
  steps:
    - uses: pagerduty_notify
```

`from_plugin:` is a positive assertion: an empty source plugin (e.g.
webhook ingress, scheduler tick) never matches a route that declares
one. Use this to keep a hook pipeline silent for unrelated plugins
without smuggling the filter through `if:` against `payload.plugin`.

`from_plugin:` and `if:` compose. The selector is checked first; the
predicate runs only when the source plugin matches.

```yaml
- name: page-on-high-severity-claude-failure
  on-hook: job.failed
  from_plugin: claude_harvest
  if:
    path: payload.severity
    op: eq
    value: high
  steps:
    - uses: pagerduty_notify
```

For multi-plugin scoping, prefer either multiple narrowly-scoped
pipelines or an `if:` predicate against `payload.plugin`. The
single-plugin selector is intentional; a generic multi-value source
matcher is deferred.

#### Inspection

The richer compiled-route shape (including `source_plugin` and `if`)
is exposed via the `GET /config/view` API endpoint under the
`compiled_routes` key, keyed by pipeline name. Operators can use this
to answer:

- what signal does this route match?
- does it require a source plugin?
- what predicate is evaluated?
- what depth guard exists?

---

## 3. Step Types

Each step in a pipeline must perform exactly **one** of the following actions:

### 3.1 `uses` (Invoke Plugin)
Calls a specific plugin or alias. This is the most common step.

```yaml
steps:
  - id: download-step   # Optional: Unique ID within the pipeline
    uses: youtube-dl
```

### 3.2 `call` (Invoke Pipeline)
Calls another pipeline by name, inheriting the current baggage. This promotes logic reuse.

```yaml
steps:
  - call: standard-summarizer
```

### 3.3 `steps` (Nested Sequence)
Groups multiple steps together. Useful for organization or within a `split`.

```yaml
steps:
  - steps:
      - uses: step-1
      - uses: step-2
```

### 3.4 `split` (Parallel Fan-out)
Executes multiple steps or sub-pipelines in parallel. Branches share the same **Baggage** but otherwise execute independently. Plugins that need per-branch filesystem isolation manage it themselves (e.g. via `mktemp -d`).

```yaml
steps:
  - uses: processor
  - split:
      - uses: discord-notifier
      - uses: s3-archiver
```

### 3.5 `relay` (Remote Event Relay)
`relay` delivers a projected event to a named remote Ductile instance. The step is declarative: it refers to `relay-instances.yaml` by stable instance name and does not expose URLs or secrets in pipeline logic.

```yaml
steps:
  - id: relay-to-lab
    relay:
      to: lab
      event: backup.ready
      dedupe_key: payload.archive_id
      with:
        archive_id: payload.archive_id
        archive_path: payload.archive_path
        checksum: payload.checksum
      baggage:
        trace_id: context.trace_id
```

Rules:
- `to` is the outbound relay instance name.
- `event` is the remote event type.
- `dedupe_key` is optional and resolves from `payload.*` or `context.*`.
- `with` is the remote event payload projection. If omitted, the current event payload is relayed.
- `baggage` is an optional explicit projection into the relay envelope baggage.

### 3.6 `if` (Conditional Step Execution)
A step may include an optional structured `if` object. Authored conditions compile into an internal `core.switch` hop. The switch evaluates the condition against the current scope and then either dispatches the gated step or bypasses it without spawning the gated plugin.

`if` must be exactly one of:
- atomic predicate: `path`, `op`, optional `value`
- `all: [...]`
- `any: [...]`
- `not: <predicate>`

Atomic example:

```yaml
steps:
  - uses: discord-notify
    if:
      path: payload.status
      op: eq
      value: error
```

Composite example:

```yaml
steps:
  - uses: long-video-handler
    if:
      all:
        - path: payload.kind
          op: eq
          value: video
        - path: payload.duration_sec
          op: gte
          value: 30
```

Supported operators in v1:
- `exists`
- `eq`
- `neq`
- `in`
- `gt`
- `gte`
- `lt`
- `lte`
- `contains` (case-insensitive string contains)
- `startswith` (case-insensitive string prefix)
- `endswith` (case-insensitive string suffix)
- `regex` (Go regexp full-string match; use inline flags like `(?i)` for case-insensitive patterns)

Path roots allowed in v1:
- `payload.*`
- `context.*`
- `config.*`

Semantics:
- typing is strict
- numeric operators require numeric operands
- string operators require string path values and string comparison values
- no implicit string-to-number coercion
- missing paths resolve to absent for `exists`, otherwise compare as `null`
- invalid conditions fail at pipeline load time
- branch decisions are observable as internal `ductile.switch.true` / `ductile.switch.false` events
- a `false` result bypasses the step and continues from the nearest downstream route

### 3.7 `with` (Payload Remap for `uses` Steps)
`with` lets a `uses` step add or override top-level payload keys immediately before the plugin is spawned.

```yaml
steps:
  - id: notify
    uses: discord_notify
    with:
      message: "{payload.stdout}"
      channel: "{context.origin_channel_id}"
      summary: "Build finished: {payload.status}"
```

Rules:
- `with` is only valid on `uses` steps.
- Each value is evaluated against a snapshot of the merged `payload.*` and `context.*` scope.
- `context.*` values only exist if an upstream step claimed them with `baggage`.
- A pure reference such as `{payload.count}` preserves the original type.
- A mixed template such as `Build: {payload.status}` produces a string.
- `with` entries do not see each other's output. They all read from the same pre-remap snapshot.
- Invalid paths or malformed templates fail the job. Ductile does not silently substitute `null` or `""`.

### 3.8 `baggage` (Explicit Durable Context for `uses` Steps)
`baggage` names the facts that should survive beyond the immediate plugin request. It is only valid on `uses` steps.

Payload is per-hop. A plugin may emit useful fields, but those fields are not durable unless the pipeline author claims them with `baggage`.

Plugin manifests help authors choose these mappings. Names-only
`values.consume` says what request payload names a command consumes, and
`values.emit` says what event payload names a command emits. The author still
chooses durable names:

```yaml
# plugin manifest
commands:
  - name: handle
    type: write
    values:
      consume:
        - payload.url
      emit:
        - event: content_ready
          values:
            - payload.url
            - payload.content_hash
            - payload.truncated
```

```yaml
# pipeline
steps:
  - id: summarize
    uses: fabric
    baggage:
      web.url: payload.url
      web.content_hash: payload.content_hash
      web.truncated: payload.truncated
```

```yaml
steps:
  - id: process
    uses: content_processor
    baggage:
      content.text: payload.content
      content.input_status: payload.status

  - id: notify
    uses: discord_notify
    baggage:
      processor.result: payload.result
      processor.exit_code: payload.exit_code
    with:
      message: "{payload.result}"
```

Rules:
- `baggage` is only valid on `uses` steps.
- Mapping keys are durable dotted paths such as `content.text` or `processor.result`.
- Mapping values are source expressions resolved from `payload.*` or `context.*`.
- Missing source paths fail the job or trigger. Ductile does not silently skip missing durable claims.
- Durable context is deep-accreted. A downstream step may add new paths, but may not change an inherited path to a different value.
- Repeating the same inherited value is allowed.

Bulk import is available when an object should be promoted under a named namespace:

```yaml
steps:
  - id: transcribe
    uses: whisper
    baggage:
      from: payload.metadata
      namespace: whisper
```

This imports `payload.metadata` as `context.whisper.*`. The namespace is required until plugin manifest default namespaces exist. Without a namespace, Ductile rejects the claim rather than placing generic keys at the durable root.

Use `baggage` for durable facts and `with` for the next plugin request. These are separate concerns:

```yaml
steps:
  - id: notify
    uses: discord_notify
    baggage:
      status.current: payload.status
    with:
      message: "Status changed to {payload.status}"
```

In this example, `status.current` is durable. `message` is just the request sent to `discord_notify`.

---

## 4. How Data Flows

### 4.1 Filesystem (Plugin-managed)
- Ductile core does not provision a workspace directory for jobs.
- If Step A needs to hand a file to Step B, the producing plugin
  writes to a path it chooses (e.g. under `~/.cache/<plugin>/` or a
  `mktemp -d`) and the path is propagated as baggage via `with:`.
- See `docs/PLUGIN_DEVELOPMENT.md` §9 for plugin-side guidance.

### 4.2 The Control Plane (Baggage)
- Metadata (JSON) is stored in the `event_context` database table.
- Every step receives durable context claimed by upstream steps.
- New durable facts are claimed explicitly with `baggage`.
- Existing durable paths are immutable: descendants may add new paths or repeat the same value, but may not rewrite inherited facts.
- If a step does not declare `baggage`, it contributes no new durable facts. Its event payload is still the immediate input to downstream routing and plugin execution, but it is not written into `event_context` implicitly.

### 4.3 Results & Payloads
- The event `payload` from Step A is passed to Step B as the immediate payload.
- `with` can reshape that immediate payload before the plugin is spawned.
- `baggage` can promote selected immediate payload fields into durable context.
- In `synchronous` mode, the final API response aggregates the results from every step.
- **Synthetic events:** If a pipeline step completes successfully but emits no events, Ductile routes a synthetic `ductile.step.succeeded` event to ensure downstream sequential steps are still triggered.

---

## 5. Decision Making

Ductile supports two kinds of decision making:

### 5.1 Native step gating with `if`
Use `if` when you want to decide whether a step should run based on the current payload, accumulated context, or plugin config. Internally Ductile inserts a `core.switch` decision hop so the branch is explicit and observable.

### 5.2 Event-driven branching
Ductile also supports **Event-Driven Branching**. A plugin decides the next path by choosing which event type to emit.

1.  **Step 1:** Plugin `classifier` inspects data.
2.  **Output:** Plugin emits `type: "image.detected"` or `type: "text.detected"`.
3.  **Routing:** You define two pipelines—one `on: image.detected` and one `on: text.detected`.

Use this when the plugin is making a domain decision about what happened. Use `if` when the pipeline is making a structural decision about whether a step should run.

---

## 6. Dispatcher Preflight

Before spawning a plugin process, the dispatcher runs a **preflight phase** for every job. Preflight separates orchestration decisions from plugin execution, ensuring consistent data-plane semantics regardless of whether a step is user-defined or an internal orchestration primitive such as `core.switch`.

### 6.1 Preflight Steps

Preflight executes two operations in order:

1. **Load request context** — Fetches accumulated baggage from the `event_context` table (all upstream metadata for this job's execution tree).

2. **Prepare for execution** — User-defined `uses` steps may apply `with` remaps after the governance payload/context merge. Internal `core.switch` jobs evaluate the compiled condition and emit `ductile.switch.true` or `ductile.switch.false`.

### 6.2 Preflight Outcomes

| Outcome | When | Effect |
|---------|------|--------|
| **run** | Context loaded successfully | Plugin process or internal builtin executes normally |
| **skip** | Reserved for explicit orchestration skip paths | Rare for authored `if:` pipelines |
| **fail** | Context load, remap, or builtin evaluation returns an error | Job marked `failed`; no downstream routing |

### 6.3 Conditional Branch Routing

When a compiled `if:` step is reached, the dispatcher runs the internal `core.switch` job. That job:

1. Evaluates the compiled condition against `payload.*`, `context.*`, and `config.*`.
2. Emits either `ductile.switch.true` or `ductile.switch.false`.
3. Lets the router dispatch either the gated step or the bypass path.

Successor routing still happens before the deciding job is marked terminal, preventing synchronous callers from seeing the tree as complete before all children are enqueued.

### 6.4 Preflight Events

The dispatcher emits a `job.preflight` event after preflight completes (or fails), with the following payload:

```json
{
  "job_id": "uuid",
  "plugin": "plugin-name",
  "command": "command-name",
  "decision": "run | skip | fail",
  "reason": ""
}
```

The `reason` field is empty for `run` decisions, contains the condition failure reason for `skip`, and contains the error message for `fail`. These events enable async consumers (TUI, event streams, monitoring) to distinguish orchestration decisions from plugin execution outcomes.

---

## 8. Lifecycle Hooks (`on-hook`)

Lifecycle hooks allow pipelines to trigger based on **system events** (e.g., job completion) rather than plugin-emitted events. Hook pipelines run as independent root jobs and do not inherit context from the job that triggered them.

### 8.1 DSL Syntax

Use the `on-hook:` keyword instead of `on:`. These keywords are mutually exclusive.

```yaml
pipelines:
  - name: notify-on-failure
    on-hook: job.completed
    steps:
      - uses: discord-notify
        if:
          path: payload.status
          op: neq
          value: succeeded
```

### 8.2 Supported Signals

| Signal | Triggered When |
|--------|----------------|
| `job.completed` | A root job reaches a terminal state (`succeeded`, `failed`, `timed_out`, or `dead`). |

### 8.3 Opt-in Configuration

To prevent accidental infinite loops and reduce noise, plugins must explicitly opt-in to lifecycle hooks in their configuration.

```yaml
plugins:
  my-important-plugin:
    notify_on_complete: true  # Required for on-hook: job.completed to fire
```

---

## 9. Failure States & Event Payloads

When a job fails, times out, or becomes "dead" (exceeds retries), Ductile emits specialized events. These events include enhanced payloads to simplify downstream notifications.

### 9.1 Enhanced Payload Fields

In addition to standard fields like `job_id` and `duration_ms`, failure events (`job.failed`, `job.timed_out`, `job.dead`) include:

| Field | Description | Example |
|-------|-------------|---------|
| `plugin` | The name of the plugin that failed. | `git-sync` |
| `message` | A human-readable summary of the failure. | `Job failed [git-sync]: connection reset` |
| `text` | An alias for `message` (convenience for notification plugins). | `Job failed [git-sync]: connection reset` |
| `error` | The raw error message (if available). | `connection reset` |

### 9.2 Usage in Pipelines

These fields enable simple notification steps without complex `if` logic or payload mapping:

```yaml
pipelines:
  - name: failure-announcer
    on-hook: job.completed
    steps:
      - uses: discord-notify
        if:
          path: payload.status
          op: neq
          value: succeeded
        # discord-notify automatically uses payload.message if present
```

---

## 10. Validation


Ductile performs several checks when loading pipelines:
- **Cycle Detection:** Refuses to start if a pipeline calls itself (directly or indirectly).
- **Shadowing:** Ensures two pipelines don't use the same name.
- **Dangling Calls:** Ensures every `call` references a valid pipeline name.
- **Condition Validation:** Verifies `if` trees have valid shape, supported operators, allowed roots, and safe depth/count limits.
- **Schema Validation:** Verifies the YAML structure against the official [pipelines.json](schemas/pipelines.schema.json).
