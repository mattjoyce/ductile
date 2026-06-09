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

## 5b. Privsep — system service with account users (opt-in)

The `--user` service above runs every plugin as your own uid (hygiene-only, ADR
Layer 1a). To contain a *popped* plugin — so it cannot read the gateway's secrets
or another plugin's memory — run the gateway as a **system service holding only
`CAP_SETUID`+`CAP_SETGID`** and drop each plugin to an unprivileged **account** user
(ADR Layer 1b). Templates live in [`deploy/systemd/`](../deploy/systemd/).

**The model:** the binary is never setuid — privilege is *init-conferred*. The
gateway runs as the unprivileged `ductile` account with exactly the two capabilities,
just enough to setuid each plugin to its account at spawn. A cap-only gateway holds
**no `CAP_CHOWN`**, so the accounts and their `0700` state dirs are provisioned
by the init layer (`sysusers.d` + `tmpfiles.d`); the gateway only **verifies** them
at boot and **refuses to start** if they are wrong (fail-closed).

Install once, as root:

```bash
sudo install -m0644 deploy/systemd/ductile-accounts.sysusers.conf  /etc/sysusers.d/ductile-accounts.conf
sudo install -m0644 deploy/systemd/ductile-accounts.tmpfiles.conf  /etc/tmpfiles.d/ductile-accounts.conf
sudo systemd-sysusers                                              # create ductile + account users
sudo systemd-tmpfiles --create /etc/tmpfiles.d/ductile-accounts.conf  # create 0700 account dirs
sudo install -m0644 deploy/systemd/ductile.service                /etc/systemd/system/ductile.service
sudo systemctl daemon-reload && sudo systemctl enable --now ductile
```

Match the config `accounts:` map to the provisioned uids/dirs:

```yaml
accounts:
  default:   { uid: 1001, gid: 1001, state_dir: /var/lib/ductile/accounts/default }
  untrusted: { uid: 1002, gid: 1002, state_dir: /var/lib/ductile/accounts/untrusted }
plugins:
  sys_exec:  { run_as: untrusted }   # arbitrary-command → isolated tier
  # ungranted first-party plugins fall back to the shared `default` tier
```

> **One source of truth you hand-maintain (T11).** The same `{uid, gid, state_dir}` triple
> is written in **three** places that must agree exactly: the config `accounts:` map (above),
> `ductile-accounts.sysusers.conf` (which *creates* the users), and `ductile-accounts.tmpfiles.conf`
> (which *creates* the `0700` state dirs). There is no generator — you keep them in sync by hand.
> The safety net is that the gateway **verifies the coupling at boot and refuses to start** if it
> disagrees (a `run_as:` grant naming an account absent from the map fails even earlier, at config
> *load*). So a mismatch costs you a loud crash-loop, never a silent half-confined run — but the
> coupling itself is yours to maintain. Keep the three files adjacent in review for that reason.

**The boot gate is fail-closed (ADR §5):** capability and a configured `accounts`
map must agree. A privileged gateway with no accounts, or accounts configured on a
host without the capability, **refuses to start** — never a silent run at gateway
privilege. The one escape hatch is an explicit `service.unconfined: true`. The full
truth table and the new boot-time refusals/warnings are in [§5c](#5c-privsep-config-reference).

> **Dev gotcha — root with no accounts refuses to boot, by design (T13).** If you start
> the daemon as **root** (or via `sudo`) on a box with **no `accounts:` map** — the common
> shape when poking at it locally — the boot gate sees *capability held, nothing to drop to*
> and **refuses to start**: `privsep boot gate: gateway holds the uid-drop capability but no
> accounts are configured`. This is correct, not a regression — a privileged daemon with no
> wall to build is the worst case. The one-line fix for local/dev work is the explicit, loud
> escape hatch:
>
> ```yaml
> service:
>   unconfined: true   # run at the gateway uid on purpose; logged loudly at boot
> ```
>
> Or simply don't run the daemon privileged in dev — an ordinary `--user` service with no
> `accounts:` map runs `unconfined` quietly (the dev cell of the boot gate). Reserve root +
> `accounts:` for the real enforcing deployment.

**Utility subcommands stay unprivileged:** `ductile config validate`, `secrets
keygen`, etc. run as the *caller* (the binary is not setuid), so they cannot read
the `0600` root-owned age key — exactly as intended.

**macOS (launchd): enforce is Linux-proven, macOS-pending (T12).** Privilege-dropping
enforce mode is proven only on a privileged **Linux** host so far; the launchd equivalent
and the live rollout are still open — see [[95-privsep-launchd-and-live-rollout]] (and
[MACOS_INSTALLATION.md](MACOS_INSTALLATION.md) for the install shape). Until then a Mac runs
**hygiene-only / `unconfined`** (no `accounts:` map, no drop capability under launchd), which
the boot gate permits quietly. Do not assume a Mac gateway is confined.

### Docker / Unraid — hygiene-only by default (decision, card #89)

**The Docker/Unraid image stays hygiene-only (ADR Layer 1a) — no account drop.** The
image runs `USER ductile` (unprivileged), and adopting full privsep there would
*raise* the container's privilege (run as root or with `SETUID`/`SETGID` caps),
which is the worst trade on the one host where a privileged container is most
costly. The ADR explicitly allows hygiene-only as a legitimate per-host default,
and at one author it is the honest choice.

You lose nothing silently: with **no `accounts:` map configured**, the boot gate runs
the gateway `unconfined` — exactly today's behaviour — and 1a still bounds the spawn
(env allowlist + secrets only over stdin). And the gate is **fail-closed against
misconfiguration**: if you *do* add an `accounts:` map to a container that lacks the
drop capability, the gateway **refuses to start** rather than presenting a wall it
cannot enforce. So "hygiene-only" here is a deliberate, safe state, not a silent gap.

*If you ever want full uid separation in a container* (e.g. running `sys_exec` on
Unraid): run it `--cap-add=SETUID --cap-add=SETGID` (prefer caps over root), bake
the account uids into the image, provision the per-account `0700` dirs on a persistent
volume (the tmpfiles.d equivalent), and bind-mount the `0600` age key from the host
(no TPM/Keychain in a container). That is opt-in and intentionally undocumented as a
default — the homelab floor is hygiene-only.

---

## 5c. Privsep config reference

Lookup-only companion to the §5b how-to: every privsep config key, the boot-gate
outcomes, the reserved tier names, and the failure modes — in one greppable place.
For *why* it is shaped this way, see the ADR (`Ductile - PrivSec and Secrets.md`).

### Config keys

| Key | Type | Meaning |
|---|---|---|
| `accounts:` | map (open) | Privsep account tiers, keyed by name. Each value is an account identity. Absent/empty map → boot gate decides posture (below). |
| `accounts.<name>.uid` | int > 0 | Unprivileged user id the plugin is dropped to. Never `0`/root. Provisioned by `sysusers.d`; referenced here by number. No two accounts may share a uid (false isolation). |
| `accounts.<name>.gid` | int > 0 | Unprivileged group id. |
| `accounts.<name>.state_dir` | absolute path | The account's owned, persistent `0700` dir (created by `tmpfiles.d`, reconciled/verified at boot). |
| `plugins.<name>.run_as` | string | The account this plugin is granted (names an `accounts:` entry). Empty/absent = no grant → falls back to the `default` tier, or `unconfined` if no `default` exists. The operator grants this — a plugin manifest hint is never trusted. |
| `service.unconfined` | bool (default `false`) | Boot-gate escape hatch: run plugins at the gateway uid (no drop) even on a capable/configured host. Logged loudly. The only sanctioned way to opt out of enforcement. |

### Reserved tier keywords (T14)

Two account names are matched **by name in code** — they are not ordinary tiers:

| Name | Role | Absence behavior |
|---|---|---|
| `default` | The tier an **ungranted** plugin (no `run_as:`) falls back to. | No `default` tier → ungranted plugins run **`unconfined`** (gateway uid), and the gateway **warns at boot**. |
| `untrusted` | The most-restricted tier a **fingerprint-mismatched** plugin is downgraded to (supply-chain-swap defence). | No `untrusted` tier → a fingerprint mismatch has no downgrade target, so that plugin's spawn is **terminal/no-retry** (`ErrNoDowngradeTarget`), and the gateway **warns at boot**. |

`unconfined` is **not** an account name — it is the named *no-drop state* (gateway uid).
Never define an account called `unconfined`; reach the state via an absent `accounts:`
map or `service.unconfined: true`.

### Boot-gate outcomes (capability × accounts)

Evaluated once at startup and on reload (`internal/dispatch/bootgate.go`). The drop
**capability** is `CAP_SETUID`+`CAP_SETGID` (or root); **accounts configured** means a
non-empty `accounts:` map.

| | accounts configured | no accounts configured |
|---|---|---|
| **capability held** | **enforce** — drop each plugin to its account | **REFUSE to start** — privileged daemon with nothing to drop to |
| **no capability** | **REFUSE to start** — a wall declared the host cannot build | **`unconfined`** — quiet info log (today's dev behaviour) |

The single invariant: **capability and accounts-configured must agree.** Disagreement
either direction is a fatal boot error. The one override is `service.unconfined: true`,
which forces `unconfined` from any cell and is logged loudly.

### Failure modes (when privsep stops a boot or a spawn)

| Condition | When it fires | Outcome |
|---|---|---|
| `run_as:` names an account absent from `accounts:` (typo, or no `accounts:` map at all) | config **load** | config rejected — daemon does not boot |
| capability ↔ accounts disagree (the two REFUSE cells above) | boot gate | refuse to start (unless `service.unconfined: true`) |
| secrets surface or an `accounts` `state_dir` is foreign-owned / un-tightenable | boot (filesystem reconcile) | refuse to start (all-or-refuse; never half-confined) |
| no `default` tier defined while enforcing | boot | **warn**; ungranted plugins run `unconfined` |
| no `untrusted` tier defined while enforcing | boot | **warn**; a later fingerprint mismatch has no downgrade target |
| plugin fails fingerprint attestation, `untrusted` tier exists | spawn | downgraded to `untrusted` (runs, contained) |
| plugin fails fingerprint attestation, no `untrusted` tier | spawn | `ErrNoDowngradeTarget` — **terminal, no retry** |
| the uid/gid drop itself is refused by the kernel at spawn | spawn | `ErrAccountDropFailed` — **terminal, no retry** (never falls back to gateway uid) |

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

## 11. Deploying the Vault onto an Instance

This is the runnable procedure for bringing up the **vault** (daemon-owned secret
delivery + plugin attestation) on an existing host-local instance. It is *how-to* only —
the steps to satisfy the need. For the **why** (the vault's mental model, the sole-writer
split, compose-time attestation, spawn hygiene, the backup/key pairing) see
[SECRETS.md](SECRETS.md); the short "Why this order" note at the end of this section
covers only the deploy-specific theory.

> First reference run: the Thinkpad (`matt-ThinkPad-T14s-Gen-1`), 2026-06-05.

### Order at a glance

backup → `vault_audit` migration → age key + genesis → reconcile config.yaml →
boot vault-operable posture + mint secrets over the admin socket →
**`config lock` + `plugin lock --all`** → cutover (reload activates the gateway) → verify.

**The two steps operators miss — both fail loud, both cost a crash-loop:**

- **`plugin lock --all` is separate from `config lock`.** Plugin attestation fingerprints
  are *not* sealed by `config lock`. Without them, `verify_integrity_on_boot` rejects
  every plugin at startup. (They were deliberately decoupled — config integrity and
  plugin attestation are different concerns.)
- **`validate_config_on_boot` surfaces dead config keys.** The strict admission gate
  rejects keys the lenient loader silently dropped — a misplaced `log_level`, a
  per-plugin `timeout`/`max_attempts` that belongs under `timeouts:`/`retry:`, a typo.
  Fix the keys (`config check` names them), or the daemon refuses to boot.

### Procedure

```bash
# Build the new binary per §1/§8 but STAGE it — don't install yet; run the gates
# against it while the old binary still serves.
NEW=~/staging/ductile-new                       # freshly built branch binary
CFG=~/.config/ductile
KEY=~/.config/secrets/ductile/age.key           # out-of-band; NOT inside $CFG

# 1. Rollback point: back up DB + config, and snapshot the current binary.
ductile system backup --to ~/backups/pre-vault-$(date -u +%Y%m%dT%H%M%SZ).tar.gz \
  --scope config --config "$CFG"
cp ~/.local/bin/ductile ~/backups/ductile-prev

# 2. Schema: add the vault_audit table. Idempotent, hot-safe. This is OBSERVABILITY,
#    not a boot gate — the daemon boots without it; the audit writer just fails soft.
python3 /path/to/ductile/scripts/migrate-add-vault-audit-table.py "$CFG/ductile.db"

# 3. Age key (record the public recipient) + genesis. Daemon STOPPED for genesis.
ductile secrets keygen --out "$KEY"             # mode 0600
systemctl --user stop ductile-local
"$NEW" vault init --vault "$CFG/vault.age" --key "$KEY" \
  > ~/.config/secrets/ductile/genesis.out 2>&1  # admin token printed ONCE
chmod 600 ~/.config/secrets/ductile/genesis.out # capture the token from here; it is the API credential
# Capture-and-rotate hygiene: lift the token into 0600/0700 custody (or a password
# manager), then SHRED genesis.out. If the token was ever exposed (shared channel,
# client log, screen), roll it in place — no re-genesis — while the daemon is stopped:
#   ductile vault rotate-admin-token --config "$CFG"   # mints + prints the NEW token once
# The old token dies immediately; update DUCTILE_VAULT_TOKEN before any API write.
# See SECRETS.md § "Rotating the admin token".

# 4. Reconcile config.yaml, then validate. Set:
#      secrets.age_key_file: <path to $KEY>
#      service.admission: { verify_integrity_on_boot: true, fail_on_drift: true,
#                           validate_config_on_boot: true, require_api_auth: true }
#      service.plugin_env_passthrough: [ ... ]   # only env names a plugin actually reads
"$NEW" config check --config "$CFG"             # MUST be clean — resolve every "ignored config key"

# 5. Load secrets INTO the vault. There is no offline bulk import — the daemon is the
#    sole writer, so secrets enter THROUGH it. With NO api token in config yet, the
#    staged binary boots the vault-operable / ductile-closed posture: it serves /vault/*
#    on a local unix socket WITHOUT opening the public gateway. See the credential ladder
#    in docs/adr/vault-credential-ladder.md.
SOCK="$CFG/vault-admin.sock"                     # api.management_socket (defaults beside the state DB)
ADMIN=...                                         # the admin token printed by genesis in step 3

"$NEW" system start --config "$CFG" &            # serves management-only; stop it (kill %1) when done
"$NEW" system status --config "$CFG"             # expect: boot_posture: management-only (live)

# Mint the API bearer token (operator-chosen value; read from stdin, never argv):
printf '%s' "$API_TOKEN" | "$NEW" vault set --api-url "unix://$SOCK" --token "$ADMIN" \
  --name core-api-token --pattern manual
# Migrate each existing tokens.yaml secret the same way — one vault set per secret:
#   printf '%s' "$VAL" | "$NEW" vault set --api-url "unix://$SOCK" --token "$ADMIN" --name <s> --principal <p>

kill %1                                          # stop the management-posture daemon

# Reference the api token in config so the NEXT boot resolves it and ACTIVATES the gateway:
#   api.auth.tokens: [ { secret_ref: core-api-token, scopes: [ "*" ] } ]

# 6. Seal BOTH: config files AND plugin attestation. Attestation is keyed by the vault
#    nonce, so genesis (step 3) must already be done.
"$NEW" config lock --config "$CFG"
"$NEW" plugin lock --all --config "$CFG"        # prints a confirm code
"$NEW" plugin lock --all <code> --config "$CFG" # commit with the code

# 7. Cutover: install the new binary and restart.
systemctl --user stop ductile-local
cp "$NEW" ~/.local/bin/ductile
systemctl --user start ductile-local

# 8. Verify.
journalctl --user -u ductile-local -n 40        # expect "compose-time attestation on"; no integrity/admission failure
curl -s localhost:8081/healthz                  # status ok; plugins_loaded == your pre-deploy baseline
ductile system vault-audit --config "$CFG"      # genesis + secret sets recorded
```

### Spawn-hygiene check before cutover (do not skip)

Plugins no longer inherit the gateway environment ([SECRETS.md §4](SECRETS.md)). Before
cutover, for each enabled plugin work out how it gets each secret **today**:

- delivered via `${VAR}` interpolation into its `config:` block → unaffected (it travels over stdin);
- read from the tool's own config (e.g. `fabric` reads `~/.config/fabric/.env`) → unaffected;
- read **directly from the process environment** → add that name to
  `service.plugin_env_passthrough`, or move the secret into the vault.

Catching this here is what prevents a fleet of plugins silently failing on a stripped
environment after cutover.

### Rollback

Stop the service, restore the previous binary (`~/backups/ductile-prev`), and — only if
config changed — restore `config.yaml` and `.checksums` from the backup. The additive
`vault_audit` table and the inert `vault.age` + age key are harmless to the prior binary,
so a DB restore is normally unnecessary. Confirm green on `healthz`.

### Why this order (theory)

The sequence is forced by dependency, not preference:

- **Genesis before `plugin lock`** — plugin fingerprints are keyed by the vault nonce that
  genesis seeds; you cannot attest plugins until the vault exists.
- **`config lock` ≠ `plugin lock`** — deliberately decoupled, so sealing config does not
  seal attestation and vice-versa.
- **Migration is observability, not a gate** — `vault_audit` is additive and not a required
  table; run it so the audit log is complete from the first op, but the boot never hinges
  on it.
- **The admission gates are independent levers** — `validate_config_on_boot` is the strict
  decode (it makes silently-dropped config keys loud); `verify_integrity_on_boot` is the
  `.checksums` + attestation preflight (it makes tamper/drift loud). Turning them on is
  what converts those silent failure modes into refuse-to-boot.

For the full mental model — principals, the sole-writer split, compose-time delivery,
attestation, and the backup/key pairing — see [SECRETS.md](SECRETS.md).
