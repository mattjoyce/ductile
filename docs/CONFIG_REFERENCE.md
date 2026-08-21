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
- **System Lock-in**: Moving the configuration directory breaks the lock.
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

  # DB table history retention
  job_transitions_retention: 30d   # historical job state transitions
  job_attempts_retention: 30d      # job execution attempt logs
  breaker_transitions_retention: 90d  # circuit breaker state changes

  # Concurrency & limits
  # Omit to use the default: max(1, CPU-1). Set to 1 to force global serial dispatch.
  max_workers: 4
  hook_max_depth: 4                # max on-hook lifecycle chain depth, prevents loops (default: 4)

  # Admission control: four independent gates the daemon applies at boot/reload.
  # Each defaults to false (permissive). Enable only what you need.
  admission:
    verify_integrity_on_boot: true   # run .checksums + fingerprint preflight at startup
    fail_on_drift: true              # operational config/routes drift -> reject (boot & reload)
    validate_config_on_boot: true    # require config validation to pass at startup
    require_api_auth: true           # reject an enabled API with no auth tokens
  # strict_mode: true  # DEPRECATED alias — enables all four admission gates above
  allow_symlinks: false              # permit resolving symbolic links under config/plugin roots
  # Spawn-hygiene allowlist: extra env var NAMES passed through to plugin child
  # processes, on top of the built-in minimal set (PATH, HOME, TZ, LANG, ...).
  # Secrets do NOT go here — they reach plugins via the vault `secrets` envelope
  # over stdin, never the environment.
  plugin_env_passthrough: [MY_PLUGIN_FLAG]

plugin_roots:
  - /opt/ductile/plugins
  - /opt/ductile/plugins-private

api:
  enabled: true
  listen: 127.0.0.1:8080
  management_socket: /tmp/ductile-admin.sock  # Unix socket serving /vault/* in the
                                          # management posture (BOOTSTRAP.md). Keep it
                                          # under ~104 bytes. Default when omitted:
                                          # vault-admin.sock beside the state DB.
  allowed_origins:                        # List of allowed CORS origins (empty by default)
    - http://localhost:3000
    - https://my-dashboard.example
  max_concurrent_sync: 10                 # Simultaneous blocking synchronous request semaphore
  max_sync_timeout: 5m                    # Hard cap on synchronous request timeout duration

state:
  path: ./data/state.db

# Encryption at rest + the owned secret vault (see docs/SECRETS.md).
secrets:
  age_key_file: ./age.key      # age identity that decrypts encrypted config and the vault
  vault_file: ./vault.age      # the encrypted vault blob (default: <configDir>/vault.age)

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

### 4.1.1 Config Key Clarifications

*   **Database/State Alias**: To support operator intuition, `database:` can be used as an interchangeable alias for `state:`. Both point to the SQLite DB:
    ```yaml
    database:
      path: ./data/state.db
    ```
*   **Path Resolution**: Relative paths (like `./data/state.db`) are resolved against the directory containing `config.yaml`. A leading `~` or `~/` expands to the user's home directory in all path fields (`state.path`, `plugin_roots`, `secrets.*`, `api.management_socket`, `tcc_paths`, `environment_vars.include`); the `~user` form is not supported.
*   **Deduplication Durations**: `dedupe_ttl` matches terminal rows in `job_queue`, so `job_queue_retention` must be at least as long as `dedupe_ttl`. The defaults are both 24h.

> **Note:** the core does not provision per-job filesystem workspaces;
> the `workspace:` config section has been removed. Plugins that need a
> scratch path manage it themselves — see `docs/PLUGIN_DEVELOPMENT.md` §9.

**`service.plugin_env_passthrough`** is a list of env var *names* (not values) granted to plugin child processes on top of the built-in spawn-hygiene allowlist (`PATH`, `HOME`, `TZ`, `LANG`, …). Use it sparingly. Secrets must **not** travel this way — they are withheld from the plugin environment and delivered only through the vault `secrets` envelope over stdin (`docs/ARCHITECTURE.md` §5.5/§6.1).

**`secrets.age_key_file`** names the age identity (private key, mode 0600) used to decrypt encrypted config includes *and* the vault blob. Resolution order: `DUCTILE_AGE_KEY_FILE` env var → `secrets.age_key_file` (relative to configDir) → built-in default locations. **`secrets.vault_file`** names the encrypted vault blob; relative to configDir, defaulting to `<configDir>/vault.age`. An absent vault file means "no vault yet" (the migration/coexistence window) — not an error. The vault holds the owned secret store; see `docs/SECRETS.md` for the principal/secret model and the `ductile vault` lifecycle.

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

### 4.2.2 Inspecting effective plugin defaults

Per-plugin `retry`, `timeouts`, `circuit_breaker`, `parallelism`, and
`max_outstanding_polls` defaults live in code (single-sourced), and unset fields
resolve to those defaults at runtime — so a plugin stanza that omits them (or sets
only part of a block) does not show the values actually in force. Use the
effective view to see what runs, with each value tagged `explicit` (set in your
file) or `default` (inherited from code):

```
$ ductile config show --effective birda
plugin: birda
  enabled: true (explicit)
  retry.max_attempts: 4 (default)
  retry.backoff_base: 30s (default)
  timeouts.poll: 1m0s (default)
  timeouts.handle: 5m0s (explicit)
  ...
  parallelism: 8 (default)        # inherits service.max_workers
```

`ductile config get --effective birda.timeouts.handle` resolves a single field
(`5m0s (explicit)`); add `--json` for `{ "value": ..., "source": ... }`. Plain
`config show` (no `--effective`) is unchanged and renders the config as written.

### 4.3 webhooks.yaml (High Security)

Webhook definitions in standalone `webhooks.yaml` can be authored in two formats:

**Option A: Documented Nested Form (Recommended)**
```yaml
webhooks:
  endpoints:
    - name: github
      path: /webhook/github
      plugin: github-handler
      secret_ref: github_webhook_secret
      signature_header: X-Hub-Signature-256
      max_body_size: 1MB
```

**Option B: Legacy Flat Form (Preserved for compatibility)**
```yaml
webhooks:
  - name: github
    path: /webhook/github
    plugin: github-handler
    secret_ref: github_webhook_secret
    signature_header: X-Hub-Signature-256
    max_body_size: 1MB
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
