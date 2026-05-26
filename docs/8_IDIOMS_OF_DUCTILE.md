# The 8 Idioms of Ductile

These are the design rules. Not preferences — the discipline that keeps
Ductile small enough to reason about and predictable enough for an agent to
drive. When a change feels off, it usually violates one of these; when
proposing a new plugin, pipeline DSL feature, or core surface, name the
idioms it serves.

The 8 are arranged: **foundation → flow → discipline → audience.** Read
them in order; later idioms presuppose earlier ones.

For why Ductile exists at all, see [`../CONSTITUTION.md`](../CONSTITUTION.md).
For the contributor mechanics, see [`../AGENTS.md`](../AGENTS.md). This
document is the authoring contract those two reference.

---

## Foundation — what the system is

### 1. Every unit of work is a queued job.

There is no other path. Webhooks, schedules, API calls, plugin output
routes, even `ductile run <plugin>` invocations from the CLI — all enqueue
into the single SQLite-backed FIFO and dispatch from there.

**Why.** A single source of truth makes execution deterministic,
retryable, and observable by construction. Side channels (a "quick" direct
call that bypasses the queue) destroy all three properties at once: you
lose the retry budget, the trace, and the ledger entry that RCA depends on.

**How it shows up.** `internal/queue/queue.go` is the only writer to
`job_queue`. Every producer — scheduler, webhook receiver, router, CLI —
funnels through `Enqueue`. No code path serves a plugin's output without
queueing first.

### 2. Spawn-per-command.

No daemon plugins. For every command: fork the plugin's entrypoint, write
JSON to its stdin, read JSON from its stdout, kill the process. Lifetime
is one invocation.

**Why.** This single decision buys language agnosticism, crash isolation,
and the absence of shared mutable plugin state in one move. There are no
zombie processes to manage, no plugin memory leaks to chase, no protocol
versions to negotiate over a long-lived connection. A plugin that breaks
breaks one job; the supervisor stays up.

**How it shows up.** `internal/dispatch/dispatcher.go` calls
`spawnPlugin` with a fresh subprocess per invocation. Timeouts are
SIGTERM → 5s grace → SIGKILL. The protocol v2 envelope is the entire
contract between core and plugin.

### 3. Core owns orchestration; plugins own side-effects.

Routing, fan-out (`split:`), conditional branching (`if:`), payload
remapping (`with:`) — all live in YAML pipelines, evaluated by the core.
Plugins do domain work and emit facts. Plugins do not decide what runs
next.

**Why.** Orchestration must be inspectable: a human reads the pipeline
file in `git diff`, an agent reads it via the API, and both see the same
flow. If orchestration lives inside plugins, neither can audit it
without reading source in three languages. Side-effects are necessarily
messy and plugin-local; orchestration is necessarily structural and
shared.

**How it shows up.** Sprint 6 compiled authored `if:` predicates into an
internal `core.switch` hop in the dispatcher. Plugin code does not branch
on payload to decide downstream routing; that decision is in YAML. The
legacy `plugins/switch/` reference plugin remains for compatibility but
its own manifest names it "Legacy payload classifier. Prefer pipeline
`if:` conditions for new authoring."

---

## Flow — how data moves

### 4. Events are the contract; payloads are the currency.

Event types are stable, named, and routed. Payloads carry the data, shaped
by the producing plugin's declared `fact_outputs`. Renaming an event is a
breaking change. Adding a new event is not.

**Why.** Stable contracts let plugins evolve independently. A consumer
that subscribes to `github.webhook.pull_request` should not care that
the underlying GitHub plugin is now in version 0.7.x. The event type is
the join key; the payload shape is documented in the manifest.

**How it shows up.** The router (`internal/router/`) matches on event
type only — exact match, no wildcards. Payload validation against
`fact_outputs` is the producer's responsibility at emit time.

### 5. Value, state, and identity are kept separate.

`config` is a value (static, env-interpolated, immutable for the run).
`plugin_facts` is an append-only series of values (the durable record of
what a plugin observed). `plugin_state` is a derived view (a
compatibility/cache projection of the latest fact). `pipeline` is an
identity (a stable name for a series of executions; its runs are values).
`job` is an identity (a queued unit; its status is state).

**Why.** Conflating these is the code path that breaks under retry and
crash. The classic mistake is "let's just update the row" — but the
question is whether the row is a value (you can't update; you append a
new one) or state (you can, with care). The plugin_facts vs plugin_state
split is this idiom made concrete: facts are values you append; state is
the latest-value view rebuilt from them.

**How it shows up.** `internal/state/` separates the two storage shapes.
The manifest's `compatibility_view` declaration tells the core how to
project facts into a state view for backward compatibility. Pipelines
have stable names (identity); pipeline runs are immutable records
(values).

---

## Discipline — how to extend it

### 6. Idempotent by design.

At-least-once delivery is the contract. Every command must be safe to
repeat. Plugins that need uniqueness use a `dedupe_key` on the event,
not an "exactly-once" myth.

**Why.** Retries are guaranteed by the architecture: the queue replays
crashed jobs, the scheduler can fire twice if the clock jitters, the
webhook receiver may see the same delivery twice if the sender retries.
A plugin that corrupts state on the second call is a plugin that will
corrupt state in production. There is no path to "exactly once" — only
"idempotent + retried."

**How it shows up.** The router carries `dedupe_key` through the event
envelope; the queue deduplicates pending jobs by `(plugin, dedupe_key)`
within the active window. Plugins that mutate external state are expected
to either accept duplicate calls cleanly or use the dedupe key
themselves.

### 7. Composable over configurable.

Many small plugins chained with `with:` remap > one plugin with twenty
flags. Many short pipelines > one long pipeline with eighteen
conditionals. Configuration surfaces grow forever; composition surfaces
grow only where there's a real new shape.

**Why.** "Simple is the goal, not easy." A plugin with a giant option
matrix looks easy ("you can do anything!") but is hard to reason about,
test, and operate. A small plugin with one job composes with others to
do the same work, but each piece is independently verifiable.

**How it shows up.** The pipeline `with:` step was added specifically to
avoid the proliferation of one-off plugin aliases that differ only in
how they relabel their input. Step-level remapping does the relabeling;
the underlying plugin stays focused.

---

## Audience — who operates it

### 8. Every surface is agent-drivable.

NOUN ACTION CLI, OpenAPI on every endpoint, structured JSON I/O,
queryable execution ledger, exit codes that mean something. No path
through Ductile requires reading source or clicking a canvas. The agent
is the primary operator; the human is the auditor.

**Why.** This is the alignment paragraph in the Constitution made
practical. Observability is not a feature here — it's the substrate this
idiom rests on. A surface that an agent cannot drive blind (CLI without
machine-readable output, an endpoint without OpenAPI, a config knob
without schema) violates the alignment.

**How it shows up.**
- CLI: `ductile <noun> <action>`; `--output json` on every read command.
- API: every endpoint listed in `/openapi.json`; `/skills` registry
  enumerates pipelines as discoverable tools.
- Diagnostics: `GET /system/doctor`, `GET /system/selfcheck`,
  `GET /stopwatch/{plugin}` (p50/p95/p99), `GET /topology` (plugin-
  signal-plugin graph).
- Ledger: every job, every step, every plugin invocation persisted in
  SQLite; queryable directly or via `ductile inspect`.

If you add a feature whose only interface is a curl command an agent has
to construct from documentation, you've violated this idiom. Add the
OpenAPI entry, the CLI verb, the structured exit code.

---

## What's not in the 8 (deliberately)

These appear in older docs or look idiom-shaped but are not rules:

- **"Ductile is upstream and downstream."** A capability claim, not a
  design rule. Lives in the capabilities list, not here.
- **"Switch decides; plugins implement."** Superseded by idiom 3
  (core owns orchestration). The Switch plugin is legacy; `if:` predicates
  are the authoring concept.
- **"Observability is a feature."** Subsumed by idiom 8 — observability
  isn't an *added* feature, it's how the agent-drivable surfaces work at
  all.
- **"Workflow logic belongs in the plugin."** The pre-Sprint-6 version
  of idiom 3. Inverted by current architecture; left here as a warning.

---

## What this list is for

Two readers:

**A pipeline or plugin author** (human or agent) uses these as a checklist.
Does this connector hold state across invocations? (Violates 2.) Does
this YAML stash branching logic inside the plugin's config? (Violates 3.)
Does this command have a `--quiet` flag but no JSON output mode?
(Violates 8.)

**A reviewer of changes to the core** uses these as the bar. A PR that
adds a long-lived plugin connection (violates 2), or a route-by-string-
matching feature (violates 4), or a new endpoint without an OpenAPI
schema (violates 8), is a PR that needs to defend the deviation, not
land quietly.

When in doubt, check against the [Constitution](../CONSTITUTION.md)
pillar the change is supposed to serve. An idiom that doesn't fit any
pillar is probably wrong; a pillar a change doesn't serve is probably
the wrong pillar.
