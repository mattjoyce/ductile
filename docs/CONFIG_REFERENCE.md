# Ductile: Configuration Specification

**Version:** 1.1 (Tiered Directory Model)  
**Date:** 2026-02-25  
**Status:** Approved  

This document defines the configuration structure, integrity verification, and runtime compilation behavior for Ductile.

---

## 1. Directory Structure

Ductile uses a configuration directory, typically located at `~/.config/ductile/`. Only
`config.yaml` is implicitly loaded; all other files must be referenced via `include:`.

```
~/.config/ductile/
├── config.yaml                  # [Operational] Service-level settings
├── webhooks.yaml                # [High Security] Webhook endpoints & secrets (include explicitly)
├── tokens.yaml                  # [High Security] API token registry (include explicitly)
├── relay-instances.yaml         # [Operational] Outbound named relay targets (include explicitly)
├── relay-ingress.yaml           # [Operational] Inbound trusted relay peers (include explicitly)
├── routes.yaml                  # [Operational] Global routing rules (include explicitly)
└── scopes/                      # [High Security] Token scope definitions
    ├── admin-cli.json
    └── github-integration.json
```

---

## 2. Tiered Integrity Preflight

Before starting, the system verifies all files against a monolithic `.checksums` manifest located in the configuration root. Integrity is enforced in two tiers:

| Tier | Files | Missing/Mismatch Behavior |
| :--- | :--- | :--- |
| **High Security** | `tokens.yaml`, `webhooks.yaml`, `scopes/*.json` | **Hard Fail**: System refuses to start (EX_CONFIG). |
| **Operational** | `config.yaml`, `routes.yaml`, `relay-instances.yaml`, `relay-ingress.yaml` | **Warn & Continue**: Logs a warning but loads the file (Unless `service.admission.fail_on_drift: true` is set, in which case it is a **Hard Fail**). |

### 2.1 The Seal (`.checksums`)
The `.checksums` file is a YAML manifest containing BLAKE3 hashes indexed by the **absolute path** of every authorized file.
- **System Lock-in**: Moving the configuration directory breaks the seal.
- **Authorization**: The `ductile config lock` command is the only way to update the manifest.

---

## 3. Monolithic Compilation (Grafting)

At runtime, the gateway compiles all discovered files into a single, monolithic configuration object.

### 3.1 Merge Logic
- **Root First**: `config.yaml` is loaded first as the base.
- **Explicit Includes**: Additional files are loaded from the `include:` list (and any directories listed there) in order.
- **Precedence**: Later entries override earlier ones (n-1 branching).
- **Matching Branches**:
    - **Maps (e.g., `plugins:`)**: Keys are merged. Duplicate keys are overridden by the later file.
    - **Arrays (e.g., `pipelines:`, `routes:`)**: Items are **appended** to the list.
    - **Scalars**: Later values replace earlier ones.

### 3.2 Modular Example
**config.yaml (Root)**
```yaml
include:
  - pipelines.yaml

service:
  name: my-gateway
```

**pipelines.yaml**
```yaml
pipelines:
  - name: video-wisdom
    on: discord.link
```

**Resulting Monolith:**
```yaml
service:
  name: my-gateway
pipelines:
  - name: video-wisdom
```

### 3.3 Directory includes

`include:` entries may point at directories. Ductile loads `*.yaml` files
from that directory (non-recursive) in alphabetical order and merges them
as if they were listed explicitly.

### 3.4 Naming convention for operator-facing instance identifiers

When config introduces an operator-facing identifier for a Ductile
instance, peer, or similarly named runtime endpoint, use lower-case
hyphenated names:

- `home-primary`
- `lab`
- `vps-backup`

Do not use:

- underscores: `home_primary`
- spaces: `home primary`
- mixed case: `HomePrimary`

Recommended pattern:

```text
^[a-z0-9]+(?:-[a-z0-9]+)*$
```

Rationale:

- reads cleanly in YAML and logs
- maps directly to URL path segments
- avoids competing conventions for operator-facing identities
- keeps names distinct from Go identifiers and internal field names

`service.name` is an operator-facing identity field and should follow
this convention when it names a concrete Ductile instance rather than a
generic service label.

---

## 4. File Formats

### 4.1 config.yaml (Service settings)
```yaml
service:
  name: ductile
  tick_interval: 60s
  log_level: info
  log_format: json
  dedupe_ttl: 24h
  job_log_retention: 30d
  job_queue_retention: 24h
  # Omit to use the default: max(1, CPU-1). Set to 1 to force global serial dispatch.
  max_workers: 4
  # Admission control: four independent gates the daemon applies at boot/reload.
  # Each defaults to false (permissive). Enable only what you need.
  admission:
    verify_integrity_on_boot: true   # run .checksums + fingerprint preflight at startup
    fail_on_drift: true              # operational config/routes drift -> reject (boot & reload)
    validate_config_on_boot: true    # require config validation to pass at startup
    require_api_auth: true           # reject an enabled API with no auth tokens
  # strict_mode: true  # DEPRECATED alias — enables all four admission gates above

plugin_roots:
  - /opt/ductile/plugins
  - /opt/ductile/plugins-private

api:
  enabled: true
  listen: 127.0.0.1:8080

state:
  path: ./data/state.db

# macOS-only. Each path is stat()-ed once on cold start (after PID lock,
# before "ductile running" log). Triggers any pending TCC popup for the
# Files-and-Folders service that gates the path. Runs synchronously while
# the operator is at the keyboard for the deploy. No-op on non-darwin and
# when the list is empty. Skipped on SIGHUP reload (binary cdhash
# unchanged → existing grants still valid).
#
# Configure local-volume paths only. An unreachable network mount blocks
# os.Stat for the filesystem-level timeout (seconds to minutes) and
# delays gateway readiness during the cold-start prewarm.
tcc_paths:
  - /Users/me/Documents/Obsidian          # triggers Documents grant
  - /Volumes/Projects                      # triggers NetworkVolumes grant
```

Relative paths (like `./data/state.db`) are resolved against the directory containing `config.yaml`.

`dedupe_ttl` uses recent terminal rows in `job_queue`, so `job_queue_retention`
must be at least as long as `dedupe_ttl`. The defaults are both 24h.

> **Note:** the core does not provision per-job filesystem workspaces;
> the `workspace:` config section has been removed. Plugins that need a
> scratch path manage it themselves — see `docs/PLUGIN_DEVELOPMENT.md` §9.

`plugin_roots` is the multi-root setting.

Discovery behavior:
- Duplicate roots are ignored after first occurrence.
- Roots are scanned in order; if duplicate plugin names exist across roots, the first discovered plugin is kept and later duplicates are ignored.

### 4.2 Plugin definitions (included file)
```yaml
plugins:
  echo:
    enabled: true
    parallelism: 1
    notify_on_complete: true # Opt-in to job.completed lifecycle signals
    schedules:   # Optional; omit for event-driven plugins
      - id: default
        every: 5m
    config:
      message: "Hello"
```

### 4.2.1 Concurrency controls

- `service.max_workers`: Global worker cap across all plugins. If omitted,
  Ductile uses `max(1, CPU-1)`. Set this to `1` to force whole-system serial
  dispatch.
- `plugins.<name>.parallelism`: Per-plugin concurrency cap.
- Constraint: `1 <= parallelism <= max_workers`.

Manifest interaction:
- Plugins may declare `concurrency_safe: false` in `manifest.yaml`; omitted
  means `true`.
- The manifest hint is the plugin author's safety declaration. Operators use
  `plugins.<name>.parallelism` to choose how much same-plugin concurrency to
  allow within the global `service.max_workers` cap.

### 4.3 webhooks.yaml (High Security - Experimental)

> [!IMPORTANT]  
> Webhook support is currently in early development and may not be fully functional in the current MVP.

```yaml
webhooks:
  - name: github
    path: /webhook/github
    plugin: github-handler
    secret_ref: github_webhook_secret
    signature_header: X-Hub-Signature-256
```

See [WEBHOOKS.md](WEBHOOKS.md) for full configuration details, include-mode caveats, and signing examples.

---

## 4.4 tokens.yaml (High Security)
```yaml
tokens:
  - name: admin-cli
    key: ${ADMIN_API_KEY}
    scopes_file: scopes/admin-cli.json
    scopes_hash: blake3:a3f8c2d9...
```

---

## 4.5 routes.yaml (Operational - Experimental)

> [!IMPORTANT]  
> Global routing rules via `routes.yaml` are experimental. Most users should prefer the `pipelines` DSL for orchestration.

```yaml
routes:
  - from: source-plugin
    event_type: event.name
    to: target-plugin
```

---

## 4.6 relay-instances.yaml (Operational - Experimental)

`relay-instances.yaml` defines named outbound Remote Event Relay targets.

```yaml
instances:
  - name: lab
    enabled: true
    base_url: https://lab.example
    ingress_path: /ingest/peer/home-primary
    secret_ref: relay-lab-v1
    key_id: v1
    timeout: 10s
    allow:
      - backup.ready
      - report.generated
```

Notes:
- `name` is the stable operator-facing alias used by sender-side config.
- `base_url` must be an absolute `http` or `https` URL.
- `ingress_path` is the receiver path that accepts the trusted relay request.
- `secret_ref` points at a `tokens.yaml` entry used as the shared HMAC secret.
- `allow` is an optional sender-side event-type allowlist.

---

## 4.7 relay-ingress.yaml (Operational - Experimental)

`relay-ingress.yaml` defines inbound trusted peers and the local acceptance policy for Remote Event Relay.

```yaml
remote_ingress:
  listen_path: /ingest/peer
  max_body_size: 1MB
  allowed_clock_skew: 5m
  require_key_id: true
  peers:
    - name: home-primary
      enabled: true
      secret_ref: relay-lab-v1
      key_id: v1
      accept:
        - backup.ready
      baggage:
        allow:
          - trace_id
          - requested_by
```

Notes:
- `listen_path` is the trusted relay ingress root mounted on Ductile's HTTP server.
- Relay ingress listens on `api.listen`; it does not introduce a separate listener address in Phase 1.
- `allowed_clock_skew` controls timestamp validation for replay-window hardening.
- `require_key_id` requires `X-Ductile-Key-Id` on inbound requests.
- `peers[].accept` is an optional receiver-side event-type allowlist.
- `peers[].baggage.allow` is a local policy for which remote baggage keys may seed new local root context.

Accepted relay requests are treated as fresh local root ingress events:
- the receiver performs normal local enqueue
- the receiver performs normal local exact-match routing
- no cross-instance `event_context` lineage is created

See [REMOTE_EVENT_RELAY.md](REMOTE_EVENT_RELAY.md) for a user-level guide and an end-to-end example.

---

## 5. Authentication Configuration

Ductile authentication is configured within the `api` section of the configuration (typically in `config.yaml` or a dedicated `auth.yaml`).

### 5.1 Scoped Tokens
For multi-user or production environments.
```yaml
api:
  auth:
    tokens:
      - token: admin_token
        scopes: ["*"]
      - token: readonly_token
        scopes: ["plugin:ro", "jobs:ro", "events:ro"]
      - token: operator_token
        scopes: ["plugin:rw", "jobs:rw", "events:ro"]
```

### 5.2 Token Scopes
Scopes are explicit:
- `*`: Full admin access.
- `plugin:ro`, `plugin:rw`: Plugin and pipeline trigger access.
- `jobs:ro`, `jobs:rw`: Job read/write access.
- `events:ro`, `events:rw`: Event stream access.

---

## 6. Environment Interpolation

Interpolation of `${VAR}` syntax happens **after** integrity verification but **before** YAML parsing.
- Secrets must never be stored in YAML files; use environment variables.
- Interpolation is **forbidden** in file paths (e.g., `include:` or directory walking) to ensure a static, verifiable tree.

### 6.1 Environment file includes

You can preload env vars from `.env` files before interpolation:

```yaml
environment_vars:
  include:
    - .env
```

Notes:
- Paths are resolved relative to the file declaring the include.
- Existing process environment variables are not overridden.
