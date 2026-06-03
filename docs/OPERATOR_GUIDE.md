# Operator Guide

This guide is intended for system administrators and LLM operators managing a Ductile instance. It covers day-to-day operations, monitoring, and administrative safety.

---

## 1. System Operations

### Starting the Service
The primary way to run Ductile is in the foreground:
```bash
./ductile system start
```
For production environments, we recommend using a **systemd** unit. See [Architecture](ARCHITECTURE.md#14-deployment) for an example configuration.

### Reloading Configuration
You can reload the configuration without restarting the service by sending a `SIGHUP` signal or using the CLI:
```bash
./ductile system reload
```

### Backups
`ductile system backup` writes a point-in-time snapshot to a single `tar.gz`
archive. The DB snapshot uses SQLite `VACUUM INTO`, so the gateway can stay
running.

```bash
ductile system backup --to /backups/ductile-$(date -u +%Y%m%dT%H%M%SZ).tar.gz \
  --scope config
```

Scope is a nested ladder; each level adds to the previous:
- `db` — DB snapshot only
- `config` (default) — `db` + ductile config dir
- `plugins` — `config` + every directory under `plugin_roots`
- `all` — `plugins` + every file under `environment_vars.include`

Each archive embeds a `BACKUP_MANIFEST.txt` recording ductile version, commit,
hostname, source paths, source DB sha256, included items, excluded items with
reasons, plugin-root mappings, and any boundary warnings (e.g. `api.yaml`
appearing at scope `config`, env files appearing at scope `all`). Inspect with
`tar -xzOf <archive> BACKUP_MANIFEST.txt` without re-extracting the rest.

At scope `config` or higher the archive includes the encrypted vault blob
(`vault.age`) so a restore is not secret-less, but the age key that decrypts it
is **deliberately excluded** — custody it out-of-band (e.g. a password manager)
and restore needs both. The manifest records the key as excluded with this
pairing note. Restore = unpack the archive, write the age key file back from
custody (mode 0600), then start. See `docs/SECRETS.md` §3 "Backup and restore"
for the full steps and the `rotate-key`-destroys-the-old-key discipline.

The command refuses to overwrite an existing destination — operator owns the
naming pattern and retention. For a scheduled-backup setup (systemd timer or
launchd), see `docs/DEPLOYMENT.md` §10.

### Rotating the vault key
`ductile vault rotate-key` rotates the daemon's age identity: it mints a fresh
key, re-encrypts the vault to it, and retires the old key — so the blob at rest
is readable only by the new key.

It is a **local, key-touching** operation and the daemon must be **stopped**
(it refuses while the daemon holds the PID lock). Stop the service, rotate, start:

```bash
ductile system stop
ductile vault rotate-key --config /path/to/config
ductile system start
```

The rotation is atomic and crash-safe (a dual-recipient bridge keeps the on-disk
key and blob decryptable at every step, and the new key is verified to decrypt the
new blob before the old key is retired).

**Back up the new key immediately.** The new identity is written to the configured
age key file (mode `0600`); the previous key is destroyed (no `.bak`). Copy the key
into your password manager. The vault blob and its key are a **pair**:

- `vault.age` is restorable only with the key that was current when the backup was
  taken. After a rotation the old key is gone, so any **pre-rotation** backup needs
  the old key you saved while it was current.
- Do **not** point `ductile secrets rotate` at `vault.age` — that command is for
  config bundles (e.g. `tokens.yaml`); `vault rotate-key` is the only safe path for
  the vault.

`system backup` (scope `config` or higher) now bundles the encrypted `vault.age`;
the age key stays out-of-band (see the Backups section above and `docs/SECRETS.md`
§3).

### Self-check
`ductile system selfcheck` runs four read-only invariants against the local
state DB:
- PID lock check (refuses to run while the gateway holds the lock — WAL safety)
- `PRAGMA integrity_check` on the SQLite file
- Schema validation (`ValidateSQLiteSchema`) against the embedded baseline
- `queue_terminal_freshness` — terminal-state job_queue rows older than the
  retention window (24h default) should not exist

```bash
ductile system selfcheck --json
```

Exit code 0 = healthy, 1 = at least one check failed. Use as a deploy gate
between binary swap and re-enabling the service.

---

## 2. Monitoring & Observability

### Real-Time Dashboard (TUI)
Ductile includes a built-in terminal UI for real-time visibility:
```bash
./ductile system watch --api-key "your-admin-token"
```

![Ductile system watch TUI](Ductile-system-watch-screenshot.png)

The watch view shows:
-   Service health, uptime, queue depth, and plugin count.
-   Metadata header (config path, binary path, version).
-   Pipelines with live status and last activity.
-   An event stream of recent activity.

### Logging
Ductile emits structured JSON logs to `stdout`. These are ideal for consumption by Logstash, Fluentd, or simple `jq` queries.
```bash
./ductile system start | jq 'select(.level == "ERROR")'
```

### SSE Event Stream
For custom monitoring tools, subscribe to the live event stream:
```bash
curl -N -H "Authorization: Bearer <token>" http://localhost:8080/events
```

---

## 3. Configuration Management

Ductile loads `config.yaml` from the config directory (typically `~/.config/ductile/`) and merges any files listed under `include:`.

### Administrative Commands
Use the `config` noun for surgical administration:
-   **Show resolved config:** `ductile config show` (includes all defaults and merges).
-   **Get a specific value:** `ductile config get plugins.echo.enabled`.
-   **Set a value safely:** `ductile config set plugins.echo.enabled=false --apply`.

### Operational Integrity (Lock & Check)
To prevent unauthorized modifications to sensitive files (like `tokens.yaml` or `webhooks.yaml`), Ductile uses **BLAKE3** hash verification. For webhook setup and signing examples, see [WEBHOOKS.md](WEBHOOKS.md).

1.  **Authorize changes:** After editing config files, update the hashes:
    ```bash
    ductile config lock
    ```
2.  **Validate state:** Ductile runs an automatic check at startup. You can run it manually with:
    ```bash
    ductile config check
    ```

### Admission Control
For hardened environments, enable the `service.admission` gates in your `config.yaml`.
Each is independent — turn on only what you need:
- `verify_integrity_on_boot: true` — the system **will not start** if any file fails integrity verification at boot.
- `fail_on_drift: true` — operational config/routes drift becomes a **hard fail** (boot and reload), not just a warning.
- `validate_config_on_boot: true` — the system **will not start** if any configuration check fails (e.g., missing dependencies).
- `require_api_auth: true` — the system **requires** at least one API token if the API is enabled.

> **Deprecated:** `service.strict_mode: true` is a back-compat alias that enables all four gates at once. Prefer the explicit `admission` block; the daemon logs a warning when `strict_mode` is used.

### Managing Scoped Tokens
Create scoped API tokens by passing scopes directly or by providing a scopes JSON file:
```bash
./ductile config token create --name "my-service" --scopes "jobs:ro,plugin:rw"
```

---

## 4. API Reference

Ductile provides a REST API for programmatic control. By default, it listens on `localhost:8080`.

### Manual Triggering
You can manually enqueue any plugin command via the API:
```bash
curl -X POST http://localhost:8080/plugin/echo/poll 
  -H "Authorization: Bearer <token>" 
  -H "Content-Type: application/json" 
  -d '{"payload": {"message": "Hello from API"}}'
```

### Job Inspection
Retrieve the status and results of any job:
```bash
curl http://localhost:8080/job/<job_id> -H "Authorization: Bearer <token>"
```

For a full list of endpoints and schemas, see the [API Reference](API_REFERENCE.md).

---

## 5. Troubleshooting

-   **Failed to acquire PID lock:** Another instance is running. Check `ps aux | grep ductile`.
-   **Plugin not running:** Ensure it is `enabled: true` in `config.yaml` and has a valid `schedule`.
-   **Database is locked:** SQLite concurrency limit. Ductile uses WAL mode to mitigate this, but very high API volume may still trigger it.
-   **Tampering detected:** Configuration file was modified without running `config lock`. Run `ductile config lock` if the change was intentional.
-   **Plugin directory ignored:** If a subdirectory in your `plugin_roots` contains an entrypoint (like `run.py`) but no `manifest.yaml`, Ductile will log a warning and ignore it. Add a manifest to enable discovery.
