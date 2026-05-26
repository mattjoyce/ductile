# Ductile

**An automation runtime designed to be operated by AI agents.**

Ductile is a self-hosted automation runtime. Every surface — CLI, REST API, plugin protocol, execution ledger — is shaped so an LLM can drive it as confidently as a human can audit it. A single Go binary orchestrates polyglot plugins via a simple JSON protocol; agents schedule jobs, route webhooks, diagnose failures, RCA incidents, test plugins, and author new ones in any language the integration needs.

See the [Constitution](https://github.com/mattjoyce/ductile/blob/main/CONSTITUTION.md) for the alignment target and the five lifecycle pillars every change must serve.

---

## Where to next

<div class="grid cards" markdown>

-   :material-rocket-launch:{ .lg .middle } **Get running**

    ---

    Build the binary, point it at a config, see your first pipeline execute.

    [:octicons-arrow-right-24: Getting Started](GETTING_STARTED.md)

-   :material-brain:{ .lg .middle } **Understand the model**

    ---

    Architecture, pipelines, routing, scheduling, and the ten idioms that make Ductile feel natural.

    [:octicons-arrow-right-24: 8 Idioms](8_IDIOMS_OF_DUCTILE.md)

-   :material-robot-outline:{ .lg .middle } **Operate with an agent**

    ---

    Load the operator skill, describe a goal in natural language, let your AI author the pipeline.

    [:octicons-arrow-right-24: For Agents](for-agents/index.md)

-   :material-puzzle:{ .lg .middle } **Build a connector**

    ---

    Plugins are subprocesses speaking JSON on stdin/stdout. Any language, any tool.

    [:octicons-arrow-right-24: Plugin Development](PLUGIN_DEVELOPMENT.md)

</div>

---

## The shape of Ductile

```text
[ Trigger ] --(event)--> [ Pipeline ] --(step 1)--> [ Connector A ]
                                      --(step 2)--> [ Connector B ]
                                      --(step 3)--> [ Connector C ]
```

1. **Connectors** do the work (fetch a URL, run a shell command, send a Discord message).
2. **Schedules** or **Webhooks** trigger the first event.
3. **Pipelines** react to events and chain connectors together, passing data downstream.
4. **The Queue** ensures every step is retried on failure and tracked in real-time.

## Core capabilities

- **AI-First Surfaces** — `/skills` registry, auto-generated OpenAPI, `/topology` plugin graph, `/stopwatch/{plugin}` latency aggregation, `/system/doctor` and `/system/selfcheck` for HTTP-driven observability. Agents drive the full lifecycle without GUIs.
- **Polyglot Runtime** — Connectors in Python, Bash, Node.js, Go, or Rust. Read `stdin`, write JSON to `stdout`, you have a plugin.
- **Event-Driven Pipelines** — Multi-step workflows with automatic baggage propagation.
- **Smart Scheduling** — `cron`, fuzzy intervals, jitter to avoid thundering herds.
- **Secure Webhooks** — Inbound HMAC-verified endpoints for GitHub, Discord, custom services.
- **Resilient Queue** — SQLite-backed, at-least-once delivery, automatic recovery and retry of orphaned jobs after crash.
- **Local & Private** — Zero-ops, single-binary, your hardware.

## Project

- Source: [github.com/mattjoyce/ductile](https://github.com/mattjoyce/ductile)
- License: [Apache 2.0](https://github.com/mattjoyce/ductile/blob/main/LICENSE)
- Changelog: [CHANGELOG.md](https://github.com/mattjoyce/ductile/blob/main/CHANGELOG.md)
