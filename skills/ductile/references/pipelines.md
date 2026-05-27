# Ductile: Pipeline DSL & Orchestration

## Overview

Pipelines are YAML-defined, event-driven workflows. They are loaded via `include:` in config.yaml and define sequential or branching flows across plugins.

## Basic Syntax

```yaml
pipelines:
  - name: video-wisdom
    on: discord.video_received     # Root trigger event type
    steps:
      - id: downloader
        uses: yt-dlp-plugin        # Execute plugin

      - id: extractor
        uses: whisper-ai           # Next step in chain
```

## Execution Modes

### Asynchronous (default)
Pipeline fires and forgets. API returns `202 Accepted` immediately.

### Synchronous
API blocks until pipeline completes (useful for Discord bots, interactive tools):

```yaml
  - name: video-summarizer
    on: discord.command.summarize
    execution_mode: synchronous
    timeout: 3m                    # Returns 202 if exceeded
    steps:
      - uses: youtube-dl
      - uses: whisper-ai
      - uses: fabric-summarizer
```

Use `?async=true` on the API call to force async behavior on synchronous pipelines.

## Orchestration Primitives

### Reusable Middleware (`call`)
```yaml
  - name: standard-summarization
    on: audio.ready
    steps:
      - uses: whisper-ai
      - uses: llm-summarizer

  - name: discord-flow
    on: discord.link
    steps:
      - uses: downloader
      - call: standard-summarization   # Inherits baggage
```

### Parallel Branching (`split`)
```yaml
    steps:
      - uses: processor
      - split:
          - uses: discord-notifier
          - uses: s3-archiver          # Both execute in parallel
```

### Event-Based Routing (`on_events`)
Ductile avoids if/else logic. Plugins decide by emitting different event types:

```yaml
- id: checker
  uses: quality-filter
  on_events:
    quality_high: [publisher]
    quality_low: [reviewer]
```

## Trigger Mechanisms

1. **Schedule** — Scheduler tick (default 60s) triggers plugin `poll` commands via cron-like `schedules:` in plugin config
2. **API** — `POST /pipeline/{name}` or `POST /plugin/{name}/{command}`
3. **Webhook** — Inbound HTTP endpoint triggers plugin `handle` command
4. **Internal routing** — Plugin emits event → gateway routes to next plugin/pipeline

## Plugin Protocol

Plugins receive via stdin:
```json
{
  "protocol": 2,
  "job_id": "uuid",
  "context": {
    "origin_user": "matt",
    "channel_id": "123"
  },
  "event": {
    "type": "video_downloaded",
    "payload": {"filename": "lecture.mp4"}
  }
}
```

Plugin rules:
- Read `context` for baggage (IDs, metadata)
- Manage your own filesystem state (see `docs/PLUGIN_DEVELOPMENT.md` §9); the core does not provision a workspace dir
- Only return **filenames** in JSON payload, never file content
- `origin_*` context keys are immutable (audit trail)

Plugin response via stdout:
```json
{
  "status": "ok",
  "events": [{"type": "video_downloaded", "payload": {"filename": "out.mp4"}}],
  "state_updates": {"last_video_id": "abc123", "last_processed_at": "2026-03-03T10:00:00Z"},
  "logs": [{"level": "info", "message": "processed"}]
}
```

`state_updates` carries the plugin's full snapshot for this invocation. When
the manifest declares a matching `fact_outputs` rule, core records the
snapshot append-only in `plugin_facts` and rebuilds the compatibility view
(`plugin_state`) automatically. See `docs/PLUGIN_FACTS.md`.

## Plugin Manifest (manifest.yaml)

```yaml
name: my-plugin
version: 0.1.0
protocol: 2
entrypoint: main.py
description: "What this plugin does"
commands:
  - name: poll
    type: write
    description: "Fetch data on schedule"
    input_schema:
      type: object
      properties:
        url: {type: string}
  - name: handle
    type: write
    description: "Process inbound events"
  - name: health
    type: read
    description: "Health check"
```

## Governance Hybrid (Data Flow)

**Control Plane** (SQLite `event_context` table):
- Stores baggage metadata across all hops
- `origin_*` keys immutable once set
- Accumulated context passed to every plugin in the chain

**Filesystem** (plugin-managed):
- The core does not provision a per-job workspace.
- Plugins create their own scratch (`mktemp -d`) or persistent cache
  (`~/.cache/<plugin>/`) and propagate paths via `with:` baggage when
  step-to-step file passing is needed.

## Safety Features

- **Cycle detection**: At load time, circular pipeline references are detected and refused
- **Hop limit**: Max 20 hops to prevent infinite loops
- **Fault isolation**: Plugin failures don't affect core or other plugins
- **At-least-once**: Orphaned jobs recovered on restart (re-queued up to max_attempts)

## Troubleshooting

```bash
# Inspect job lineage and baggage
ductile job inspect <job_id> -v

# Check system health
ductile system status --json

# Validate config after changes
ductile config check

# Live monitoring TUI
ductile system watch
```

Job inspect output shows:
```
[1] <root> :: <entry>
    context_id : uuid-ctx-1
    baggage    : {"origin_user": "matt"}
    artifacts  : [video.mp4]

[2] video-wisdom :: step_process
    context_id : uuid-ctx-2
    parent_id  : uuid-ctx-1
    baggage    : {"origin_user": "matt", "status": "processed"}
    artifacts  : [video.mp4, summary.txt]
```
