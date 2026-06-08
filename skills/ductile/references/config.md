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
  admission:                   # four independent boot/reload gates (each defaults to false)
    verify_integrity_on_boot: true   # run .checksums + plugin-fingerprint preflight at startup
    fail_on_drift: true              # promote config/routes drift from warning to admission failure
    validate_config_on_boot: true    # require config validation to pass at startup
    require_api_auth: true           # reject an enabled API with no auth tokens
  # strict_mode: true          # DEPRECATED alias — enables all four admission gates above
  plugin_env_passthrough: [MY_PLUGIN_FLAG]  # extra env var NAMES for plugin children (allowlist; NOT for secrets)

plugin_roots:
  - /opt/ductile/plugins       # Scanned in order; first match wins

api:
  enabled: true
  listen: 127.0.0.1:8080

state:
  path: ./data/state.db        # Relative to config.yaml location

secrets:
  age_key_file: ./age.key      # age identity decrypting config + vault (env DUCTILE_AGE_KEY_FILE overrides)
  vault_file: ./vault.age      # encrypted vault blob (default: <configDir>/vault.age)

include:
  - api.yaml
  - plugins.yaml
  - pipelines.yaml
  - webhooks.yaml
```

**Secrets & encryption at rest.** `secrets.age_key_file` is the age private key
(mode 0600) that decrypts both encrypted config includes and the vault blob
(`secrets.vault_file`). Key resolution: `DUCTILE_AGE_KEY_FILE` → `age_key_file` →
default locations. The vault is the owned secret store: register principals and
set secrets with `ductile vault` (see SKILL.md). `service.plugin_env_passthrough`
allowlists extra env var *names* for plugin children — secrets do **not** travel
via env (they reach plugins over stdin). Full model: `docs/SECRETS.md`.

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

API bearer tokens are **vault-only** (#94, ADR §8.5 — "if it's a secret, it's in
the vault"). Each token MUST carry a `secret_ref` naming a vault secret; the value
is resolved from the vault at boot. A literal `token:` value or `${ENV}`
interpolation is **rejected at load** — fail-closed, no migration window. The
standalone `tokens.yaml` file surface was retired in epic #48; a token carries its
`scopes` array directly:

```yaml
api:
  auth:
    tokens:
      - secret_ref: ductile-api-admin       # resolved from the vault at boot
        scopes: ["*"]
      - secret_ref: ductile-api-readonly
        scopes: ["plugin:ro", "jobs:ro", "events:ro"]
```

Provision the referenced secrets with `ductile vault set <name> ...` (the vault
must be unlocked before the API listener opens; a missing or empty `secret_ref`
is a hard boot error, not a request-time failure).

Custom scope definitions still live in `scopes/*.json` (High Security tier —
walked at discovery and integrity-checked at lock/boot).

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
      secret_ref: astro_webhook_secret       # Resolved from the vault (ductile vault set)
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

## Inspecting Effective Config

Per-plugin `retry`, `timeouts`, `circuit_breaker`, `parallelism`, and
`max_outstanding_polls` defaults live in code (single-sourced) and unset fields
resolve to those defaults at runtime — so a stanza that omits them (or sets only
part of a block) does not show the values actually in force. Use `--effective` to
see what runs, each value tagged `(explicit)` (set in your file) or `(default)`
(inherited from code):

```bash
ductile config show --effective birda          # whole plugin, every field tagged
ductile config get  --effective birda.timeouts.handle   # one field; add --json for {value, source}
```

Plain `config show` / `config get` (no `--effective`) render the config as
written, unchanged. The runtime resolver and this view share one source, so the
view always matches what the dispatcher and scheduler actually use.

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
