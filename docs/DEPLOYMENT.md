# Ductile Deployment Guide

This document describes how to deploy a host-local Ductile instance as a
systemd user service. It reflects the first reference deployment on
`matt-ThinkPad-T14s-Gen-1` (2026-02-22) and is the canonical procedure for
repeating this on other hosts.

See also: RFC-006 (local execution plane topology).

---

## 1. Build the Binary

Build from source and install to the user's local bin:

```bash
cd /path/to/ductile
go build -o ~/.local/bin/ductile ./cmd/ductile
```

Verify:
```bash
ductile --version
```

The binary is self-contained — no additional runtime dependencies.

---

## 2. Directory Layout

Create a deployment root with separate `config/` and `data/` directories:

```
ductile-local/
├── config/
│   ├── config.yaml      # main config (includes others via `include:`)
│   ├── api.yaml         # API listen address + auth tokens
│   └── plugins.yaml     # plugin enable/config
└── data/
    ├── ductile.db       # SQLite state DB (created on first start)
    └── outputs/         # write target for file_handler plugin
```

Create it:
```bash
mkdir -p ~/admin/ductile-local/config ~/admin/ductile-local/data/outputs
```

---

## 3. Split Config Pattern

Ductile supports modular ("grafted") configs via the `include:` key. The main
config file sets global options and includes the others by relative path.

### config/config.yaml

```yaml
log_level: info

state:
  path: ./data/ductile.db

plugin_roots:
  - /path/to/ductile/plugins

include:
  - api.yaml
  - plugins.yaml
```

`plugin_roots` is a list of directories to scan for plugin executables at
startup. Any plugin binary found here is *discovered*; only plugins listed in
`plugins.yaml` are *configured* (and those not listed emit a warning but still
load).

### config/api.yaml

```yaml
api:
  enabled: true
  listen: "localhost:8081"
  auth:
    tokens:
      - token: <your-token>
        scopes: ["*"]
```

Generate a token:
```bash
openssl rand -hex 32
```

Store the token in your shell environment:
```bash
# ~/.zshrc
export DUCTILE_LOCAL_TOKEN=<your-token>
```

### config/plugins.yaml

```yaml
plugins:
  fabric:
    enabled: true
    timeout: 120s
    max_attempts: 2
    config:
      FABRIC_DEFAULT_PATTERN: "summarize"

  file_handler:
    enabled: true
    timeout: 30s
    max_attempts: 1
    config:
      allowed_read_paths: "${HOME}"
      allowed_write_paths: "${HOME}/ductile-local/data/outputs"

  jina-reader:
    enabled: true
    timeout: 30s
    max_attempts: 3
    circuit_breaker:
      threshold: 3
      reset_after: 5m
    config: {}
```

---

## 4. Validate Config

Before starting the service, validate the configuration:

```bash
cd ~/admin/ductile-local
ductile config check --config config/config.yaml
```

Expected output:
```
Configuration valid (N warning(s))
  WARN  [unused] plugin "echo" discovered but not referenced in config
  ...
```

Warnings about undeclared plugins are expected if `plugin_roots` contains
plugins you haven't explicitly configured. They are loaded but not usable
without config entries.

---

## 5. systemd User Service

Create `~/.config/systemd/user/ductile-local.service`:

```ini
[Unit]
Description=Ductile Gateway (local prod)
After=network.target

[Service]
Type=simple
WorkingDirectory=${HOME}/ductile-local
ExecStart=${HOME}/.local/bin/ductile system start --config config/config.yaml
Restart=on-failure
RestartSec=5s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
```

Enable and start:
```bash
systemctl --user daemon-reload
systemctl --user enable --now ductile-local
```

Check status:
```bash
systemctl --user status ductile-local
journalctl --user -u ductile-local -f
```

---

## 6. Verification Checklist

After starting the service, verify the following:

```bash
# Health — no auth required
curl http://localhost:8081/healthz

# Expected:
# {"status":"ok","uptime_seconds":N,"queue_depth":0,"plugins_loaded":5,"plugins_circuit_open":0}

# Plugin list — requires auth
curl -H "Authorization: Bearer $DUCTILE_LOCAL_TOKEN" http://localhost:8081/plugins

# OpenAPI schema — no auth required
curl http://localhost:8081/openapi.json | head -20
```

Confirm:
- [ ] `status: ok` in healthz
- [ ] `plugins_loaded` > 0
- [ ] fabric, file_handler, jina-reader appear in `/plugins`
- [ ] `/openapi.json` returns valid JSON

---

## 7. RFC-006 Topology Notes

RFC-006 defines two Ductile instance roles:

| Role | Purpose |
|---|---|
| **Boundary node** | Public-facing gateway, handles external API calls, auth, routing |
| **Host-local node** | Per-host execution plane, runs plugins with local resource access |

This deployment is a **host-local node**:
- Listens on `localhost` only (not exposed to LAN)
- Token scoped to `["*"]` for local use
- Plugins have access to local filesystem (`file_handler`) and local tools (`fabric`)
- Receives work dispatched from a boundary node or local AgenticLoop agent

The prod Unraid instance (`192.168.20.4:8888`) is the boundary node for this network.

---

## 8. Updating the Binary

When a new version is built:

```bash
# Stop the service first (optional but clean)
systemctl --user stop ductile-local

# Rebuild
cd /path/to/ductile
go build -o ~/.local/bin/ductile ./cmd/ductile

# Restart
systemctl --user start ductile-local
systemctl --user status ductile-local
```

Or just rebuild and restart in one shot — the service will pick up the new
binary on next start:
```bash
cd /path/to/ductile && go build -o ~/.local/bin/ductile ./cmd/ductile && systemctl --user restart ductile-local
```

## 9. Schema Migrations Before Deploy

If a release adds additive SQLite schema, apply the matching migration script to
the existing state DB before the normal deploy/restart.

This is especially relevant for instances that already have a populated
database. The binary still carries the mono-schema for fresh databases, but the
preferred operational path for existing databases is to run explicit migrations
first so schema changes are intentional and visible in deployment steps.

For non-empty existing databases, Ductile validates schema on startup instead
of silently adding missing upgrade-era tables or indexes. If the DB is behind,
startup should fail with a migration hint rather than mutating the schema
implicitly.

## 10. Backups

`ductile system backup` writes an atomic, point-in-time snapshot of the SQLite
state DB plus selected runtime artefacts into a single `tar.gz` archive. The
DB snapshot is taken via `VACUUM INTO`, which is safe under concurrent writers
— no service stop required.

```bash
ductile system backup --to <file.tar.gz> [--scope SCOPE] [--config PATH]
```

The four scopes are a nested ladder; each level adds to the previous:

| Scope | Contents |
|---|---|
| `db` | `VACUUM INTO` snapshot of the state DB only |
| `config` (default) | `db` + ductile config dir (`config.yaml`, `api.yaml`, `plugins.yaml`, `pipelines.yaml`, `webhooks.yaml`, `.checksums`) + the encrypted vault blob `vault.age` (the age key is **excluded** — out-of-band custody; restore needs both) |
| `plugins` | `config` + every directory under `plugin_roots` (excludes `.git`, `node_modules`, `.venv`, `venv`, `__pycache__`, `.DS_Store`, `*.pyc`, `*.pyo`) |
| `all` | `plugins` + every file referenced under `environment_vars.include` |

Each invocation prints its INCLUDED / EXCLUDED list to stdout before doing the
work and embeds a `BACKUP_MANIFEST.txt` inside the archive recording the same
information plus ductile version, commit, hostname, source paths, source DB
sha256, and any boundary warnings (e.g. `api.yaml` at `config` scope, env files
at `all` scope).

Refuses to overwrite an existing destination — operator owns naming and
retention via shell glue.

### Scheduled backups

systemd-timer (Thinkpad pattern) — `~/.config/systemd/user/ductile-backup.service`:

```ini
[Unit]
Description=Ductile backup snapshot

[Service]
Type=oneshot
Environment=BACKUP_DIR=%h/admin/ductile-backups/thinkpad/auto
ExecStart=/bin/sh -c 'mkdir -p "$BACKUP_DIR" && \
  STAMP=$(date -u +%%Y%%m%%dT%%H%%M%%SZ) && \
  %h/.local/bin/ductile system backup \
    --to "$BACKUP_DIR/ductile-$STAMP.tar.gz" --scope config && \
  find "$BACKUP_DIR" -name "ductile-*.tar.gz" -mtime +7 -delete'
```

Paired timer `~/.config/systemd/user/ductile-backup.timer`:

```ini
[Unit]
Description=Nightly ductile backup at 03:00 local

[Timer]
OnCalendar=*-*-* 03:00:00
Persistent=true

[Install]
WantedBy=timers.target
```

Enable:
```bash
systemctl --user daemon-reload
systemctl --user enable --now ductile-backup.timer
```

launchd (Mac pattern) — equivalent `LaunchAgent` plist with `StartCalendarInterval`
runs the same command sequence; see existing `com.mattjoyce.ductile-local.plist`
as a template for the `ProgramArguments` shape.

Pre-migration backups before any breaking schema change are a separate manual
invocation under `~/admin/ductile-backups/<instance>/pre-<slug>-<timestamp>/`
— they sit outside the auto-rotation directory.
