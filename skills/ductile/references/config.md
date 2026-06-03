# Ductile: Configuration Reference

## Directory Structure

```
~/.config/ductile/
├── config.yaml        [Operational] Service settings (auto-loaded)
├── api.yaml           [Operational] API/auth settings (include explicitly)
├── plugins.yaml       [Operational] Plugin definitions (include explicitly)
├── pipelines.yaml     [Operational] Pipeline definitions (include explicitly)
├── routes.yaml        [Operational] Global routing rules (include explicitly)
├── webhooks.yaml      [High Security] Webhook endpoints & secrets
├── tokens.yaml        [High Security] API token registry
├── scopes/            [High Security] Token scope definitions
│   └── admin-cli.json
└── .checksums         BLAKE3 hash manifest (managed by `config lock`)
```

Only `config.yaml` is auto-loaded; everything else must be referenced via `include:`.

## config.yaml

```yaml
service:
  name: ductile
  tick_interval: 60s           # Scheduler loop interval
  log_level: info              # debug | info | warn | error
  log_format: json             # json | text
  dedupe_ttl: 24h
  job_log_retention: 30d
  strict_mode: true            # Hard-fail on any integrity mismatch

plugin_roots:
  - /opt/ductile/plugins       # Scanned in order; first match wins

api:
  enabled: true
  listen: 127.0.0.1:8080

state:
  path: ./data/state.db        # Relative to config.yaml location

include:
  - api.yaml
  - plugins.yaml
  - pipelines.yaml
  - webhooks.yaml
```

## Modular Grafting (Merge Strategy)

Files listed in `include:` are loaded in order and merged into a single monolithic config:
- **Maps** (e.g., `plugins:`): Keys merged; later files override duplicate keys
- **Arrays** (e.g., `pipelines:`, `routes:`): Items appended
- **Scalars**: Later values replace earlier

`include:` may point to directories; Ductile loads `*.yaml` files non-recursively in alphabetical order.

## Plugin Definition

```yaml
plugins:
  echo:
    enabled: true
    timeout: 30s
    max_attempts: 3
    schedules:
      - id: default
        every: 5m          # 5m|15m|30m|hourly|2h|6h|daily|weekly|monthly
        jitter: 30s
        preferred_window: "06:00-22:00"   # Optional time constraint
    config:
      message: "Hello"     # Plugin-specific static config
```

## Tokens & Scopes

### tokens.yaml (High Security)
```yaml
tokens:
  - name: admin-cli
    key: ${ADMIN_API_KEY}          # env var interpolation
    scopes_file: scopes/admin-cli.json
    scopes_hash: blake3:a3f8c2d9...
```

### Inline tokens (api.yaml / config.yaml)
```yaml
api:
  auth:
    tokens:
      - token: test_admin_token
        scopes: ["*"]
      - token: readonly_token
        scopes: ["plugin:ro", "jobs:ro", "events:ro"]
```

### Available Scopes
- `*` — Full admin access
- `plugin:ro` / `plugin:rw` — Plugin and pipeline trigger access
- `jobs:ro` / `jobs:rw` — Job read/write
- `events:ro` / `events:rw` — Event stream

## Webhooks (webhooks.yaml — High Security)

```yaml
webhooks:
  endpoints:
    - name: astro_rebuild_staging
      path: /webhook/astro-rebuild-staging
      plugin: astro_rebuild_staging
      secret_ref: astro_webhook_secret       # References tokens.yaml
      signature_header: X-Ductile-Signature-256
      max_body_size: 1MB                     # Optional, default 1MB
```

Webhook listener port set separately in config.yaml:
```yaml
webhooks:
  listen: 127.0.0.1:8082
```

## Environment Interpolation

`${VAR}` syntax is supported everywhere except `include:` paths.
Preload `.env` files:
```yaml
environment_vars:
  include:
    - .env
```
Existing process env vars are NOT overridden.

## Integrity Workflow

```bash
# After a config FILE change (config.yaml, api.yaml, pipelines, webhooks):
ductile config check          # Validate first (catches YAML errors, policy violations)
ductile config lock           # Update .checksums to authorize the new file state

# After a PLUGIN manifest/entrypoint change (separate, decoupled act):
ductile plugin lock <name>    # Re-attest the plugin's bytes (re-records its fingerprint)
```

The `.checksums` file holds BLAKE3 hashes keyed by absolute path **and** the
recorded plugin fingerprints. `config lock` re-hashes the config files and
*preserves* the plugin fingerprints; it does **not** re-attest plugin bytes — that
is the separate `ductile plugin lock`. A plugin whose bytes changed without a
re-`plugin lock` is refused its vault secrets at spawn (fail closed), and a routine
`config lock` will not fix it. Moving the config directory breaks the seal —
re-lock after moving.
