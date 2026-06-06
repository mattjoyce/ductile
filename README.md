# Ductile

[![Go Version](https://img.shields.io/badge/go-1.25.0-blue.svg)](https://golang.org)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

**An automation runtime AI agents can run, debug, and build for.**

Ductile runs local automation pipelines at personal scale (~50–500 jobs/day). Every surface — CLI, REST API, plugin protocol, execution ledger — is shaped so an AI agent can drive it as confidently as a human can audit it. Agents don't just trigger jobs; they diagnose failures, reconstruct incidents, test plugins, and author new ones in any language.

---

## How it works

Connectors (plugins) produce and consume events. Pipelines chain them. A queue handles retries.

```text
[ Schedule / Webhook ] → event → [ Pipeline ] → step 1 → [ Connector A ]
                                               → step 2 → [ Connector B ]
                                               → step 3 → [ Connector C ]
```

1. **Connectors** do the work — fetch a URL, run a shell command, call an API, send a notification.
2. **Schedules** or **Webhooks** fire the first event.
3. **Pipelines** react to events, chain connectors, and pass data (payload) between steps.
4. **The Queue** ensures every step is retried on failure and tracked in the execution ledger.

---

## Quick Start

```bash
# Clone and build
git clone https://github.com/mattjoyce/ductile.git
cd ductile
go build -o ductile ./cmd/ductile

# Copy the example config, then set DUCTILE_TOKEN_ADMIN in config.yaml
cp -R ./config ~/.config/ductile

# Start the gateway
./ductile system start
```

The `echo` plugin in `plugins/echo/` runs every 5 minutes by default — use it to confirm the scheduler and queue are working before wiring up real connectors.

See [Getting Started](docs/GETTING_STARTED.md) for a complete walkthrough.

---

## Example pipelines

### YouTube to knowledge base
Fetch new playlist videos, transcribe, AI-summarize, save to a file, notify Discord.

```yaml
pipelines:
  - name: playlist-to-knowledge-base
    on: youtube.playlist_item
    steps:
      - uses: youtube_transcript
      - uses: fabric
      - uses: file_handler
      - uses: discord_notify
```

### GitHub policy guard
On every pull request, run a policy check and alert on violations.

```yaml
pipelines:
  - name: github-policy-guard
    on: github.webhook.pull_request
    steps:
      - uses: repo_policy
      - uses: discord_notify
        if: payload.policy_failed == true
```

### Content-triggered site rebuild
Watch a folder for new markdown files, rebuild only when something changed.

```yaml
plugins:
  folder_watch:
    schedules:
      - every: 1m
    config:
      root: ./content/summaries
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

## Capabilities

- **Polyglot plugins** — Write connectors in Python, Bash, Node.js, Go, Rust, or anything that reads stdin and writes stdout JSON.
- **Event-driven pipelines** — Chain connectors into multi-step workflows with automatic payload propagation between steps.
- **Step-level payload remap** — Use `with:` mappings to adapt inputs downstream without creating one-off plugin aliases.
- **Cron and fuzzy schedules** — `cron`, intervals, and jitter to avoid thundering herds.
- **HMAC-verified webhooks** — Inbound endpoints for GitHub, Discord, or any custom service.
- **Parallel dispatch** — Bounded worker pool with per-plugin concurrency caps.
- **Plugin aliasing** — Run multiple instances of the same connector (e.g., three different Discord channels) without duplicating code.
- **SQLite queue** — At-least-once delivery; recovers orphaned jobs after a crash.
- **AI-first surfaces** — `/skills` registry, auto-generated OpenAPI, `/topology` plugin graph, `/stopwatch/{plugin}` latency aggregation, `/system/doctor`, and `/system/selfcheck`.
- **Local and private** — Single binary, no cloud dependency. State lives in files an agent can `ls` and a human can `git diff`.

---

## For AI operators

Ductile ships skill manifests so agents have structured ways to operate it. Copy skills to your agent's skills directory:

```bash
cp -r skills/ductile/ ~/.claude/skills/ductile/
```

| Skill | Pillar | Purpose |
|---|---|---|
| [`skills/ductile/`](skills/ductile/) | Run | Operate, configure, deploy |
| [`skills/ductile-rca/`](skills/ductile-rca/) | RCA | Root cause analysis from the execution ledger |
| [`skills/ductile-plugin-developer/`](skills/ductile-plugin-developer/) | Author | Build plugins to the manifest contract |

Planned: `ductile-doctor` (Debug) and `ductile-plugin-tester` (Test).

---

## Documentation

- [**Getting Started**](docs/GETTING_STARTED.md) — From zero to a running pipeline.
- [**Cookbook**](docs/COOKBOOK.md) — Recipes for Discord, YouTube, Astro, and more.
- [**8 Idioms of Ductile**](docs/8_IDIOMS_OF_DUCTILE.md) — How to think in Ductile.
- [**Constitution**](CONSTITUTION.md) — Why Ductile is shaped this way and what it refuses to be.
- [**Architecture**](docs/ARCHITECTURE.md) — Technical deep dive.
- [**Plugin Development**](docs/PLUGIN_DEVELOPMENT.md) — Build your own connectors.
- [**Database Reference**](docs/DATABASE.md) — Schemas and useful SQL queries.

## Contributing

- [**AGENTS.md**](AGENTS.md) — Design lenses, vocabulary, Go quality bar.
- [**CONTRIBUTING.md**](CONTRIBUTING.md) — Build, test, and PR mechanics.

---

## License

Apache 2.0. See [LICENSE](LICENSE) for details.

## Changelog

See [CHANGELOG.md](CHANGELOG.md).
