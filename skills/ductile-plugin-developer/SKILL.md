---
name: ductile-plugin-developer
description: >
  Author ductile plugins and compose pipelines with reverence for the manifest
  contract, protocol v2, fact_outputs, and the 8 idioms of ductile. Use when the
  user wants to: write a new ductile plugin, modify an existing plugin, design a
  manifest, declare fact_outputs, pick watcher vs poller vs writer vs transformer
  pattern, compose a pipeline DSL, route events, or iterate on plugin behavior
  locally. Trigger keywords: "write a ductile plugin", "plugin manifest",
  "fact_outputs", "protocol v2", "pipeline DSL", "event routing", "manifest
  rejected", "fact_outputs not landing", "plugin returns wrong output". Operates
  the value/state/identity gate before code is written, applies Hickey's
  decomplecting discipline, follows Armstrong's isolation and make-it-work /
  make-it-beautiful / make-it-fast cadence. Pairs with `ductile` (to deploy after
  authoring) and `ductile-rca` (only when broken in incident-mode, not iteration).
---

# Ductile Plugin Developer

This skill is **Pillar 5: Author** of the Ductile Constitution (see
[`../../CONSTITUTION.md`](../../CONSTITUTION.md)) — write plugins that survive
contact with the queue, the supervisor, retries, crashes, and public-repo
sanitization. It is opinionated in the same direction `AGENTS.md` is
opinionated: **simple is the goal, not easy.**

> **Decision rule.** When two designs are on the table, ask: which one introduces
> fewer concepts? Fewer concepts wins, even if it requires more typing. Easy is
> familiar; simple is fewer-things-braided-together. The braided one always
> looks easier on day one and harder on day thirty.

## Seam: when to also load

| Companion | When |
|---|---|
| `ductile` | You are about to deploy a plugin you wrote here, run `config check` / `config lock`, reload a gateway, or move a plugin from Mac → Thinkpad → Unraid. |
| `ductile-rca` | A plugin is failing in **incident mode** — production-like, symptoms not understood. *Not* for "I just wrote this and it doesn't work yet" — that is iteration, stay here. |

Load all three when shipping a fix to a live incident: rca to understand, this
skill to author the fix, `ductile` to lock and reload.

## Out of scope

- Instance-specific deploy steps → `ductile`
- Live job triage and hypothesis design → `ductile-rca`
- CLI/operator commands → `ductile`

## Step 0 — The value / state / identity gate

**Refuse to write code until this is answered.** This is Hickey's value/state/identity
distinction made operational. Conflating these is the bug that breaks under retry
and crash.

For each piece of data your plugin touches, name it:

| Kind | Definition | Where it lives | Example |
|---|---|---|---|
| **value** | Immutable fact at a point in time | `plugin_facts` (append-only) | "Withings reported 81.2 kg at 2026-05-12T07:00Z" |
| **state** | A place that changes | `plugin_state` (compatibility view), an external file, a sibling container's DB | "current latest weight row" |
| **identity** | A stable name for a series of values over time | `plugin alias`, `pipeline name`, `event type` | "the `withings` plugin", "the `weight-roundtrip` pipeline" |

If you cannot name which one you are dealing with, you are about to braid them
together. That is the bug. Stop and decompose.

**Rule:** Producers emit values. Views project state. Identities are config.

## Step 1 — Pick the archetype, do not blend

Decomplect — these four archetypes vary independently in the real world.
A plugin that blends two of them will not retry safely.

| Archetype | Observes / acts on | Returns | Example |
|---|---|---|---|
| **watcher** | filesystem mtime, inotify | events about *change* | `folder_watch`, `file_watch` |
| **poller** | external API or device | `fact_outputs` snapshot | `withings`, `garmin` |
| **writer** | downstream system | side-effect + ack | webhook send, db insert |
| **transformer** | inbound payload | outbound payload | `echo`, `switch`, format conversion |

Resist combining. A "watcher that also polls" is two plugins. Compose them in a
pipeline instead. *Plugins stay dumb; the core controls flow.* (Idiom 1.)

## Step 2 — The manifest is data, not config

Hickey: data > functions > macros. Your `manifest.yaml` is the contract; treat it
as a value the core consumes, not a config sidecar. Every directive in the
manifest exists to push you toward the correct shape. Wanting to do something
the manifest will not sanction is usually a signal to step back, not work around.

Minimum manifest shape:

```yaml
name: <plugin alias — the identity>
protocol: 2
entrypoint: ./plugin.sh        # any executable
commands:
  - poll                       # what the scheduler will call
  - handle                     # what the router will call on events
  - health                     # for selfcheck / triage
concurrency_safe: true|false   # may core run me in parallel with myself?
```

If your plugin needs durable memory, add `fact_outputs` with a `compatibility_view`
declaration. The core will record the snapshot append-only and rebuild the view
automatically. You never write to `plugin_state` directly.

Full reference: `docs/PLUGIN_DEVELOPMENT.md` (913 lines, canonical) and
`docs/PLUGIN_FACTS.md`.

## Step 3 — Protocol v2 in one screen

Spawn-per-command. **One JSON in, one JSON out, then exit.** No daemon, no shared
memory, no in-process state across invocations.

```text
Core → Plugin (stdin, single JSON)         Plugin → Core (stdout, single JSON)
{                                           {
  protocol: 2,                                status: "ok" | "error",
  job_id, command,                            result: "<short summary>",
  config,         # static, read-only         error: "<msg if error>",
  state,          # compatibility view row    retry: true|false,
  context,        # baggage, immutable        events: [],
  event,          # only for `handle`         state_updates: {},
  deadline_at                                 logs: []
}                                           }
```

Read everything you need from this envelope. **Do not reach for ambient state.**
The `context` baggage is your only memory of the upstream lineage; respect
`origin_*` keys — the core will refuse to let you overwrite them.

## Step 4 — Effects at the edges, pure core

Hickey: I/O at the boundary, logic in the middle. Even in a 50-line bash plugin
this discipline pays. Structure:

1. Parse stdin once → a record.
2. Compute the response purely from that record.
3. Perform any side effects (poll the API, write the file) in a narrow function.
4. Write stdout once, exit.

If you cannot test the middle without the edges, you have braided them.

## Step 5 — Let it crash. Don't smuggle errors.

Armstrong: the gateway is your supervisor. Plugin crashes do not corrupt other
plugins. Your job is to **fail clearly so the supervisor can do the right thing.**

```text
Recoverable problem  →  { status: "error", retry: true,  error: "<reason>" }
Permanent problem    →  { status: "error", retry: false, error: "<reason>" }
Catastrophic         →  exit non-zero. The core sees stderr; the next invocation
                        is fresh.
```

Anti-patterns:
- Swallowing exceptions to "keep the plugin alive". You are *supposed* to die.
- Writing partial state on the way out. Either the fact is complete or it never happened.
- Long-running retry loops inside one invocation. The queue is the retry primitive.

> *Errors must be detected by some non-faulty entity.* (Armstrong) — the **core**
> is that entity. Make its job easy by being honest about failure.

## Step 6 — Make it work, then make it beautiful, then make it fast

In that order. (Armstrong.) For a ductile plugin specifically:

1. **Work**: smallest possible `poll` that returns one correct fact. No
   `fact_outputs` yet, just stdout JSON.
2. **Beautiful**: add the manifest declarations — `fact_outputs`,
   `compatibility_view`, `concurrency_safe`. Let the core do the work the
   manifest enables.
3. **Fast**: only now consider parallelism (`parallelism` in plugin config),
   batching, partial polling. If your plugin is already correct under serial
   dispatch, going parallel is a config change, not a rewrite.

## Step 7 — Pipelines: small folds, not big graphs

(Idiom 6: composable over configurable. Idiom 5: plugins own orchestration; core
owns execution.)

Compose pipelines from small steps. One step should answer one question or
produce one fact. If a step needs `if`/`split`/`call` in the same node, that's a
braid; split it.

```yaml
# Decomplected
steps:
  - uses: withings.poll                # produce fact
  - if: event.weight_changed           # gate
    call: notify-weight-pipeline       # delegate
```

Reading order for pipeline composition: `docs/PIPELINES.md`,
`docs/ROUTING_SPEC.md`. The reference cards in this skill's `references/`
folder hold the daily-driver tables.

## Step 8 — The 8 idioms, applied to authors

The full list lives in `docs/8_IDIOMS_OF_DUCTILE.md`. The author-relevant
ones, in their canonical order:

- **Idiom 1** — *Every unit of work is a queued job.* Don't invent a side
  channel. Your plugin's output routes through the queue like any other.
- **Idiom 3** — *Core owns orchestration; plugins own side-effects.* Don't
  branch on payload inside your plugin to decide what runs next. That's
  what pipeline `if:` is for. Your plugin does the domain work and emits
  facts.
- **Idiom 4** — *Events are the contract; payloads are the currency.*
  Stabilize event names early; rename = breaking change. Document the
  payload shape via `fact_outputs`.
- **Idiom 5** — *Value, state, and identity are kept separate.* What your
  plugin observes is an append-only fact (a value). The "latest" is a
  derived view. Don't update in place.
- **Idiom 6** — *Idempotent by design.* Your contract with the queue.
  Every command safe to retry without side effects. Use `dedupe_key` if
  you need uniqueness.
- **Idiom 7** — *Composable over configurable.* Two small plugins beat
  one option-heavy plugin. "Simple is the goal, not easy" made concrete.
- **Idiom 8** — *Every surface is agent-drivable.* Emit `logs[]`
  generously, write a readable manifest, declare `fact_outputs` shape.
  Future-you and `ductile-rca` will thank you.

## Step 9 — Sanitization gate (pre-commit)

`ductile-plugins` is a **public** repository. Before any commit:

- [ ] No tokens, API keys, OAuth secrets in code, manifest, or examples.
- [ ] No host-specific paths (`/Users/mattjoyce`, `/mnt/user/appdata/...`,
      `/Volumes/Projects/...`).
- [ ] No instance names (`Mac dev`, `Thinkpad`, `Unraid prod`) in examples.
- [ ] No real user identifiers (emails, account IDs, device serials).
- [ ] No `~/.config/ductile/` snippets containing live data.
- [ ] Examples use placeholders: `<TOKEN>`, `<HOST>`, `<USER>`, `${ENV_VAR}`.
- [ ] `.env`, `.checksums`, and any local lock files are gitignored.

If your example needs a real-looking value, invent one. The repo is read by
people who do not have your guardrails.

## Step 10 — The `config lock` handoff

You produced a manifest change. You are now done in this skill.
**Hand off to `ductile`:**

```bash
ductile config check        # in plugin-developer terms: did my manifest parse?
ductile config lock         # operator's authorization step
ductile system reload       # apply without restart
```

If symptoms appear after lock+reload, hand off to `ductile-rca`.

## References

- `references/config.md` — plugin config grafting under `plugins.<name>` keys
- `references/api.md` — REST endpoints relevant to manual plugin invocation
- `references/pipelines.md` — pipeline DSL cheat sheet
- In-repo: `docs/PLUGIN_DEVELOPMENT.md`, `docs/PLUGIN_FACTS.md`,
  `docs/PIPELINES.md`, `docs/ROUTING_SPEC.md`, `docs/8_IDIOMS_OF_DUCTILE.md`,
  `AGENTS.md` (contributor contract; already half-Hickey)
