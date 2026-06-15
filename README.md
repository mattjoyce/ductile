# Ductile

**An automation runtime designed to be operated by AI agents.**

Ductile is a local-first automation runtime for event-driven workflows.

It runs pipelines, schedules jobs, accepts webhooks, executes plugins, retries failures, and records everything in a durable execution ledger.

Unlike most automation tools, Ductile is designed around a different assumption:

> The primary operator is an AI agent.
>
> Humans configure, govern, and audit.
> AI operates.

Every surface — CLI, API, plugin protocol, execution history, topology, diagnostics, and skills — is designed so an AI agent can understand the system, operate it safely, diagnose failures, and build new automations.

---

# Why Ductile Exists

Most automation systems assume a human administrator.

They expose dashboards, forms, and visual builders because the operator is expected to be a person.

Ductile assumes the operator is increasingly an AI agent.

That changes the design.

An AI does not need a dashboard.

It needs:

- Structured APIs
- Machine-readable configuration
- Durable execution history
- Clear contracts
- Deterministic behaviour
- Good evidence when things fail

Ductile therefore optimises for:

- AI as operator
- Humans as governors and auditors
- Local ownership of data
- Durable execution lineage
- Simple composable primitives

---

# How It Works

```text
Schedule ─┐
          │
Webhook ──┼──► Event
          │
API ──────┘

Event
  │
  ▼
Pipeline
  │
  ├──► Plugin A
  ├──► Plugin B
  └──► Plugin C

Results
  │
  ▼
Execution Ledger
```

A plugin emits an event.

A pipeline reacts to the event.

The pipeline invokes one or more plugins.

Jobs are queued, retried, recorded, and tracked.

Everything is visible in the execution ledger.

---

# Quick Start

Build Ductile:

```bash
git clone https://github.com/mattjoyce/ductile.git
cd ductile

make build        # canonical (stamps the version); or: go build -o ductile ./cmd/ductile
```

Copy the example configuration:

```bash
cp -R ./config ~/.config/ductile
```

The example config ships with its admission gates on, so a fresh gateway will not
boot until it has a vault to draw secrets from. Run genesis once (the daemon
stopped), then seal and validate the config:

```bash
# Genesis: create the age key, then the vault it unlocks.
# `vault init` prints the admin token ONCE — capture it to 0600 custody.
./ductile secrets keygen --out ~/.config/ductile/age.key
./ductile vault init   --vault ~/.config/ductile/vault.age --key ~/.config/ductile/age.key

# Seal the config (lock first — scope files are checksum-verified), then check.
./ductile config lock  --config ~/.config/ductile
./ductile config check --config ~/.config/ductile
```

Start Ductile:

```bash
./ductile system start
```

> Provisioning the API bearer token (so the REST surface serves publicly) is a
> couple more vault commands — see [docs/BOOTSTRAP.md](docs/BOOTSTRAP.md) for the
> full credential ladder.

The example configuration loads:

```yaml
include:
  - api.yaml
  - plugins.yaml
  - pipelines.yaml
  - webhooks.yaml
```

Install a plugin and enable it:

```yaml
plugins:
  folder_watch:
    enabled: true

    schedules:
      - every: 1m

    config:
      watches:
        - id: summaries
          root: "~/projects/content"
          event_type: summaries.changed
```

Create a pipeline:

```yaml
pipelines:
  - name: rebuild-on-update

    on: summaries.changed

    steps:
      - id: rebuild
        uses: sys_exec
```

When the plugin emits `summaries.changed`, Ductile dispatches the pipeline and records execution in the ledger.

---

# Core Concepts

## Plugins

Plugins do work.

Examples:

- Read a folder
- Call an API
- Execute a command
- Send Discord messages
- Read GitHub repositories
- Generate AI summaries

Plugins are polyglot.

They can be written in:

- Go
- Python
- Bash
- Node.js
- Rust
- Any language capable of speaking the plugin protocol

---

## Events

Events describe something that happened.

Examples:

```text
summaries.changed
github.pull_request.opened
youtube.url.detected
backup.completed
```

Events trigger pipelines.

---

## Pipelines

Pipelines connect events to actions.

```yaml
pipelines:
  - name: github-policy

    on: github.pull_request.opened

    steps:
      - uses: repo_policy

      - uses: discord_notify
```

> `repo_policy` and `discord_notify` are not bundled in core — they ship as
> separate plugins (the `ductile-plugins` and `ductile-discord` repos). Install a
> plugin under a `plugin_root`, then enable it in `plugins.yaml`. The core repo
> bundles a handful of example plugins (`echo`, `sys_exec`, `folder_watch`, …) to
> get you started.

Pipelines are the preferred orchestration model.

---

## Queue

The queue provides:

- Durable execution
- Retry handling
- Crash recovery
- Concurrency control
- Execution history

If Ductile crashes, jobs survive.

---

# AI Operator Features

Ductile includes explicit affordances for AI operators.

- Skills
- OpenAPI descriptions
- Topology inspection
- Self-checks
- Doctor commands
- Execution lineage
- Structured diagnostics
- Machine-readable configuration

The goal is not merely that AI can use Ductile.

The goal is that AI can successfully administer Ductile.

---

# Capabilities

- Event-driven pipelines
- Scheduled jobs
- HMAC-verified webhooks
- Retry policies
- Circuit breakers
- Parallel execution
- SQLite state store
- Polyglot plugins
- Remote relay
- Vault-backed secrets
- Configuration integrity verification
- AI operator skills

---

# Documentation Structure

## Tutorial

Start here:

- Getting Started

Goal:

> Get Ductile running and execute your first pipeline.

---

## How-To Guides

Examples:

- Configure webhooks
- Build a Discord workflow
- Create a scheduled task
- Relay events between instances
- Deploy to a server

Goal:

> Solve a specific problem.

---

## Reference

Authoritative technical specifications:

- Configuration Reference
- Pipeline Schema
- Plugin Protocol
- Database Schema
- API Reference

Goal:

> Look up facts.

---

## Explanation

Why Ductile is built this way:

- Constitution
- Architecture
- AI-first operations
- Execution lineage
- Design principles

Goal:

> Understand the theory behind the system.

---

# License

Apache 2.0
