# Ductile

[![Go Version](https://img.shields.io/badge/go-1.25.4-blue.svg)](https://golang.org)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

**An automation runtime AI agents can run, debug, and build for.**

Ductile is an automation runtime designed to be operated by AI agents. Every surface — CLI, REST API, plugin protocol, execution ledger — is shaped so an LLM can drive it as confidently as a human can audit it. A single Go binary orchestrates polyglot plugins via a simple JSON protocol; agents schedule jobs, route webhooks, diagnose failures, RCA incidents, test plugins, and author new ones in any language the integration needs.

See [`CONSTITUTION.md`](CONSTITUTION.md) for the alignment target and the five lifecycle pillars every change must serve.

---

## Grokking Ductile in 30 Seconds

Ductile works by connecting **Connectors** (plugins) via **Pipelines** using an internal **Event Bus**.

```text
[ Trigger ] --(event)--> [ Pipeline ] --(step 1)--> [ Connector A ]
                                      --(step 2)--> [ Connector B ]
                                      --(step 3)--> [ Connector C ]
```

1.  **Connectors** do the work (fetch a URL, run a shell command, send a Discord message).
2.  **Schedules** or **Webhooks** trigger the first event.
3.  **Pipelines** react to events and chain connectors together, passing data (the "payload") between them.
4.  **The Queue** ensures every step is retried on failure and tracked in real-time.

---

## Core Capabilities

-   **Polyglot Runtime** — Write connectors in Python, Bash, Node.js, Go, or Rust. If it reads `stdin` and writes `stdout` JSON, it works.
-   **Event-Driven Pipelines** — Chain connectors into multi-step workflows. Pass data downstream with automatic metadata (baggage) propagation.
-   **Step-Level Payload Remap** — Use pipeline `with:` mappings to adapt downstream plugin inputs without creating one-off plugin aliases.
-   **Smart Scheduling** — Support for `cron`, fuzzy intervals, and jitter to avoid thundering herds.
-   **Secure Webhooks** — Inbound HMAC-verified endpoints for GitHub, Discord, or custom services.
-   **Parallel Dispatch** — Bounded worker pool with per-plugin concurrency caps and "concurrency-safe" manifest hints.
-   **Plugin Aliasing** — Run multiple instances of the same connector (e.g., three different Discord notifications) without duplicating code.
-   **Resilient Queue** — SQLite-backed, at-least-once delivery. Automatically recovers and retries orphaned jobs after a system crash.
-   **Optional TUI client** — Under redesign for v1.1 as the standalone `ductile-watch` binary; interim observability is the API and structured logs.
-   **AI-First Surfaces** — Built-in `/skills` registry, auto-generated OpenAPI, `/topology` plugin graph, `/stopwatch/{plugin}` latency aggregation, and `/system/doctor` + `/system/selfcheck` for HTTP-driven observability. Agents drive the full lifecycle without GUIs.
-   **Local & Private** — Zero-ops, single-binary architecture. Your data, your keys, your hardware.

---

## What Can You Build?

### 1. The "YouTube Wisdom" Pipeline
Automatically fetch, transcribe, and AI-summarize new videos from a playlist, then save them to your blog and notify Discord.

```yaml
# Define the workflow in pipelines.yaml
pipelines:
  - name: playlist-to-knowledge-base
    on: youtube.playlist_item
    steps:
      - uses: youtube_transcript   # Fetches raw transcript
      - uses: fabric               # AI-summarizes via LLM (Fabric)
      - uses: file_handler         # Saves markdown to your repo
      - uses: discord_notify       # Pings you when it's done
```

### 2. The "Repo Sentinel"
Monitor your GitHub repositories for new PRs, run a custom policy check (e.g., license or format), and notify your team of violations.

```yaml
pipelines:
  - name: github-policy-guard
    on: github.webhook.pull_request
    steps:
      - uses: repo_policy          # Custom script checking for README/License
      - uses: discord_notify       # Alert if policy fails
        if: payload.policy_failed == true
```

### 3. The "Astro Staging Rebuild"
Watch a local folder for new markdown files (e.g., from an AI summary pipeline) and trigger a site rebuild only when changes are detected.

```yaml
plugins:
  folder_watch:
    schedules:
      - every: 1m
    config:
      root: "./content/summaries"
      event_type: summaries.updated

pipelines:
  - name: rebuild-on-update
    on: summaries.updated
    steps:
      - uses: sys_exec
        config:
          command: "npm run build && docker restart astro-site"
```

---

## Quick Start

```bash
# 1. Build the binary
go build -o ductile ./cmd/ductile

# 2. Start the gateway (uses ./config by default)
./ductile system start
```

## Skills for AI Operators

Ductile ships skill manifests that give AI agents structured ways to operate it. Drop these into your agent's skills directory (`cp -r skills/<name>/ ~/.claude/skills/<name>/`):

-   [`skills/ductile/`](skills/ductile/) — **Pillar 1: Run.** Operate, configure, deploy.
-   [`skills/ductile-rca/`](skills/ductile-rca/) — **Pillar 3: RCA.** Root cause analysis from the execution ledger.
-   [`skills/ductile-plugin-developer/`](skills/ductile-plugin-developer/) — **Pillar 5: Author.** Build plugins to the manifest contract.

Planned: `ductile-doctor` (Pillar 2: Debug), `ductile-plugin-tester` (Pillar 4: Test).

## Documentation

-   [**Constitution**](CONSTITUTION.md) — Why Ductile exists and the five pillars (read this first).
-   [**Getting Started**](docs/GETTING_STARTED.md) — From zero to your first pipeline.
-   [**Cookbook**](docs/COOKBOOK.md) — Real-world recipes (Discord, YouTube, Astro, etc.).
-   [**8 Idioms of Ductile**](docs/8_IDIOMS_OF_DUCTILE.md) — How to think in Ductile.
-   [**Core Architecture**](docs/ARCHITECTURE.md) — The technical deep dive.
-   [**Database Reference**](docs/DATABASE.md) — Schemas and useful SQL queries.
-   [**Plugin Development**](docs/PLUGIN_DEVELOPMENT.md) — Build your own connectors.

## Contributing

-   [**CONSTITUTION.md**](CONSTITUTION.md) — Alignment target. Every change should name the pillar it serves.
-   [**AGENTS.md**](AGENTS.md) — Contributor contract: design lenses, vocabulary, Go quality bar.
-   [**CONTRIBUTING.md**](CONTRIBUTING.md) — Build, test, and PR mechanics.

---

## License
Apache 2.0. See [LICENSE](LICENSE) for details.

## Changelog
See [CHANGELOG.md](CHANGELOG.md).
