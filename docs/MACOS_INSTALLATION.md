# Ductile on macOS — Installation Guide

This guide documents the first installation of Ductile on macOS (darwin/arm64, macOS 15 Sequoia). It covers the differences from the Linux/systemd deployment documented in [DEPLOYMENT.md](DEPLOYMENT.md).

---

## Platform Differences at a Glance

| Concern | Linux | macOS |
|---|---|---|
| **Service manager** | systemd | launchd |
| **Service unit file** | `~/.config/systemd/user/*.service` | `~/Library/LaunchAgents/*.plist` |
| **Enable/start** | `systemctl --user enable --now` | `launchctl bootstrap gui/$(id -u)` |
| **Status** | `systemctl --user status` | `launchctl list \| grep ductile` |
| **Logs** | `journalctl --user -u ductile-local` | `tail -f ~/Library/Logs/ductile-local.log` |
| **User bin dir** | `~/.local/bin/` (in PATH by default) | `~/.local/bin/` (add to PATH if needed) |
| **Restart policy** | `Restart=on-failure` | `KeepAlive=true` + `ThrottleInterval` |

---

## 1. Prerequisites

- macOS 13 Ventura or later (tested on macOS 15 Sequoia, arm64)
- Go ≥ 1.24.3 — install via Homebrew: `brew install go`
- Git

Verify Go:
```bash
go version
# go version go1.26.0 darwin/arm64
```

---

## 2. Clone and Build

```bash
git clone git@github.com:mattjoyce/ductile.git ~/Projects/ductile
cd ~/Projects/ductile
go build -ldflags "$(./scripts/version.sh)" -o ductile ./cmd/ductile
```

> **Note:** On macOS, `/usr/local/bin/` requires `sudo` to write to. Install to `~/.local/bin/` instead (create it if it doesn't exist and ensure it's in `$PATH`):

```bash
mkdir -p ~/.local/bin
cp ductile ~/.local/bin/ductile

# Add to PATH if not already present — add this line to ~/.zshrc:
export PATH="$HOME/.local/bin:$PATH"
```

Verify:
```bash
ductile --version
# ductile <version>
# commit: <hash>
# built_at: <timestamp>
```

---

## 3. Config Directory

Ductile uses `~/.config/ductile/` by default (XDG-style, same as Linux).

Create the directory and a minimal working config:

```bash
mkdir -p ~/.config/ductile
```

### config.yaml

```yaml
log_level: info

service:
  strict_mode: false

state:
  # Use an absolute path — tilde expansion in this field resolves relative
  # to the config directory, not $HOME, on some ductile versions.
  path: "/Users/YOUR_USERNAME/.config/ductile/ductile.db"

plugin_roots:
  # Absolute path to the built-in plugins in the cloned source repo.
  # Tilde is NOT expanded here — use full paths.
  - "/Users/YOUR_USERNAME/Projects/ductile/plugins"

include:
  - api.yaml
  - plugins.yaml
  - pipelines.yaml
  - webhooks.yaml
```

> **Gotcha (all platforms):** `~` in `plugin_roots` and `state.path` is resolved
> relative to the config directory, not `$HOME`. Use absolute paths, or paths
> relative to the config dir.

### api.yaml

API bearer tokens are **vault-only** — a literal `token:` value is rejected at
load (#94). Boot with no tokens (the management posture), mint the token over
the management socket per [BOOTSTRAP.md](BOOTSTRAP.md) steps 4–6, then
reference it:

```yaml
api:
  enabled: true
  listen: "127.0.0.1:8082"   # Use 8082 if 8081 is taken by another ductile instance
  management_socket: /tmp/ductile-admin.sock
  # AFTER minting (BOOTSTRAP.md step 5):
  # auth:
  #   tokens:
  #     - secret_ref: core-api-token
  #       scopes: ["*"]
```

Store the minted value for API clients in your shell environment:
```bash
# ~/.zshrc
export DUCTILE_LOCAL_TOKEN=<the minted value>
```

### plugins.yaml

Start with the built-in `echo` plugin to verify the setup:

```yaml
plugins:
  echo:
    enabled: true
    schedules:
      - id: default
        every: 5m
        jitter: 30s
    config:
      message: "Hello from Ductile on Mac!"
```

### pipelines.yaml

```yaml
pipelines: []
```

### webhooks.yaml

```yaml
webhooks:
  listen: "127.0.0.1:8091"
  endpoints: []
```

---

## 4. Lock the Config

Ductile verifies config integrity via checksums. After writing all config files, lock them:

```bash
ductile config lock --config ~/.config/ductile/
# Successfully locked configuration in 1 directory/ies:
#   - /Users/YOUR_USERNAME/.config/ductile
```

Re-run this after any config change. Ductile will refuse to start if the checksums don't match.

---

## 5. Foreground Test

Verify the setup runs cleanly before installing as a service:

```bash
ductile system start --config ~/.config/ductile/
```

In another terminal:
```bash
curl http://127.0.0.1:8082/healthz
# {"status":"ok","uptime_seconds":N,"queue_depth":0,"plugins_loaded":10,...}
```

Press `Ctrl+C` to stop.

---

## 6. launchd Service

macOS uses **launchd** instead of systemd. Create a `LaunchAgent` plist for user-session auto-start:

```bash
mkdir -p ~/Library/LaunchAgents
```

Create `~/Library/LaunchAgents/com.mattjoyce.ductile-local.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.mattjoyce.ductile-local</string>

    <key>ProgramArguments</key>
    <array>
        <string>/Users/YOUR_USERNAME/.local/bin/ductile</string>
        <string>system</string>
        <string>start</string>
        <string>--config</string>
        <string>/Users/YOUR_USERNAME/.config/ductile/</string>
    </array>

    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
        <key>HOME</key>
        <string>/Users/YOUR_USERNAME</string>
    </dict>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <true/>

    <key>StandardOutPath</key>
    <string>/Users/YOUR_USERNAME/Library/Logs/ductile-local.log</string>

    <key>StandardErrorPath</key>
    <string>/Users/YOUR_USERNAME/Library/Logs/ductile-local.log</string>

    <key>ThrottleInterval</key>
    <integer>5</integer>
</dict>
</plist>
```

Replace `YOUR_USERNAME` with your macOS username (e.g. `mattjoyce`). Absolute paths are required — launchd does not expand `~`.

> **Why `ThrottleInterval: 5`?** Combined with `KeepAlive`, this prevents tight restart loops if ductile crashes on startup (e.g. config validation failure). It mirrors `RestartSec=5s` in systemd.

---

## 7. launchctl Commands

```bash
# Load and start (survives reboots)
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.mattjoyce.ductile-local.plist

# Check if running (PID in first column means running, 0/-1 means stopped/failed)
launchctl list | grep ductile

# Stop
launchctl stop com.mattjoyce.ductile-local

# Start (if already loaded)
launchctl start com.mattjoyce.ductile-local

# Unload (remove from launchd entirely)
launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/com.mattjoyce.ductile-local.plist

# View logs
tail -f ~/Library/Logs/ductile-local.log
```

> **launchd vs systemd vocabulary:**
> - `bootstrap` = `systemctl enable --now` (load and start, persist across reboots)
> - `bootout` = `systemctl disable --now` (unload and stop, remove persistence)
> - `start/stop` = `systemctl start/stop` (one-shot, already-loaded service)
> - `launchctl list` = `systemctl status` (check running state)

---

## 8. Verification Checklist

After the launchd service is running:

```bash
# Health — no auth required
curl http://127.0.0.1:8082/healthz
# {"status":"ok","uptime_seconds":N,"queue_depth":0,"plugins_loaded":N,...}

# Plugin list — requires auth
curl -H "Authorization: Bearer $DUCTILE_LOCAL_TOKEN" http://127.0.0.1:8082/plugins

# Logs
tail -20 ~/Library/Logs/ductile-local.log
```

Confirm:
- [ ] `status: ok` in healthz
- [ ] `plugins_loaded` > 0
- [ ] echo plugin appears in `/plugins`
- [ ] Log file exists at `~/Library/Logs/ductile-local.log`

---

## 9. Updating the Binary

```bash
cd ~/Projects/ductile
git pull

# Stop the service first — the running binary cannot be overwritten
launchctl stop com.mattjoyce.ductile-local

go build -ldflags "$(./scripts/version.sh)" -o ~/.local/bin/ductile ./cmd/ductile

# Restart
launchctl start com.mattjoyce.ductile-local
```

After updating config files, always re-lock before restarting:
```bash
ductile config lock --config ~/.config/ductile/
launchctl stop com.mattjoyce.ductile-local
launchctl start com.mattjoyce.ductile-local
```

### 9.1. macOS TCC pre-warm (do this on every redeploy)

Ductile is ad-hoc signed (`go build` produces a per-build cdhash). macOS TCC indexes Files-and-Folders grants by cdhash, so **every rebuild invalidates every existing TCC grant**. Plugins that touch protected paths (`~/Documents`, `/Volumes/...`, `~/Desktop`, `~/Downloads`, Full Disk) will hit a fresh permission popup the first time they access each one.

If you redeploy and walk away, an inbound job (e.g. an email arriving at 3am that triggers a plugin reading `~/Documents`) will hang on the unanswered popup until its plugin timeout fires (default 300s), then hard-fail. Logs show the plugin's whole output emitting in one burst when the timeout triggers — no flush during the block.

#### Recommended: configure `tcc_paths` in `config.yaml`

Ductile cold-start runs an explicit `os.Stat` against each path declared in `tcc_paths` before accepting work. Each `Stat` triggers any pending TCC popup for the Files-and-Folders service that gates the path. This happens synchronously while the operator is at the keyboard for the deploy, not at an arbitrary later moment when an unattended job hits the path.

```yaml
# config.yaml
tcc_paths:
  - /Users/me/Documents/Obsidian          # triggers Documents grant
  - /Volumes/Projects                      # triggers NetworkVolumes grant
```

See `docs/CONFIG_REFERENCE.md` for the full schema and notes (configure local-volume paths only — an unreachable network mount blocks `os.Stat` for the filesystem-level timeout and delays gateway readiness).

After the restart, popups appear sequentially (one per path); click **Allow** on each. Each Allow grants the new cdhash for that service. Skipped on SIGHUP reload (binary cdhash unchanged → existing grants still valid).

#### Fallback: manually trigger an access

If you haven't configured `tcc_paths` yet, or you want to re-warm a specific service without editing config + restarting, invoke any plugin that reads the protected path:

```bash
# Example: invoke a plugin that reads ~/Documents
curl -s -X POST -H "Authorization: Bearer $DUCTILE_TOKEN" \
  http://127.0.0.1:8082/plugin/<your-docs-touching-plugin>/<command> \
  -d '{"path": "/Users/YOU/Documents/some-known-file.md"}'

# Example: invoke a plugin that reads /Volumes/...
curl -s -X POST -H "Authorization: Bearer $DUCTILE_TOKEN" \
  http://127.0.0.1:8082/plugin/<your-volumes-touching-plugin>/<command> \
  -d '{"path": "/Volumes/<some-mount>/known-file.txt"}'
```

Click **Allow** on each popup. Same outcome as the `tcc_paths` cold-start — just driven by the request path instead of declared config.

### 9.2. Verify TCC state

After clicking Allow, verify the grants exist for the new binary identity:

```bash
sqlite3 ~/Library/Application\ Support/com.apple.TCC/TCC.db \
  "SELECT service, auth_value, datetime(last_modified,'unixepoch','localtime')
   FROM access WHERE client LIKE '%ductile%';"
```

`auth_value=2` is Allowed, `auth_value=0` is Denied. Each service ductile needs should be listed with `auth_value=2` and a recent `last_modified` matching when you clicked Allow.

To inspect TCC denials and prompts since the last redeploy:

```bash
/usr/bin/log show --predicate 'subsystem == "com.apple.TCC"' --last 1h \
  | grep -iE "ductile|AUTHREQ_PROMPTING"
```

A line like `Failed to match existing code requirement for subject .../ductile and service kTCCServiceSystemPolicySomething` confirms the cdhash mismatch fired a prompt for that service.

### 9.3. Long-term fix

Apple Developer ID codesigning would anchor the designated code requirement on a stable identity instead of the per-build cdhash, and grants would carry forward across rebuilds. Trade-off: $99/yr for a Developer account. Until then, treat the pre-warm step as part of the redeploy procedure, not an optional extra.

---

## 10. Enforced (privsep) Deployment — Root LaunchDaemon

Sections 1–9 install the **unconfined** dev gateway: a `LaunchAgent` that runs as *you*, in
your GUI session. That is posture #1 in [DEPLOYMENT_POSTURES.md](DEPLOYMENT_POSTURES.md).

This section covers the **enforced** posture — plugins dropped to unprivileged worker accounts
behind a uid wall (PrivSec ADR Layer 1b). On macOS that requires a **root `LaunchDaemon`**, and
the templates live at `deploy/launchd/com.mattjoyce.ductile.plist` + `deploy/install-macos.sh`.
*Proven live on MacM1, 2026-06-08 (card #95): a confined `sys_exec` dropped to `uid=1002` and a
root `0600` secret read back `Permission denied`.*

### 10.1 Why root — and why that's the only option here

launchd has **no per-capability grant** (no `CAP_SETUID`/`CAP_SETGID` like systemd). To
`setuid`-drop each plugin to its account uid, the gateway needs `euid 0`, so it runs as a **root
LaunchDaemon** (the plist has no `UserName` key → root). The Linux non-root + two-caps posture is
**unreachable** on macOS.

- The binary is **never setuid** — privilege is conferred by launchd starting the daemon as root.
- macOS's threat model differs from Linux: `task_for_pid` is SIP-gated, so a dropped worker can't
  trivially inspect the root gateway's memory. It is **still root**, though — document this posture
  honestly; do not claim Linux-identical least-privilege.

### 10.2 Layout: everything under `/opt/ductile` (NOT `/etc` or `/var`)

On macOS `/etc`, `/var`, and `/tmp` are **symlinks** to `/private/*`. Ductile's **runtime** refuses
symlinked config paths (a path-swap guard — stricter than `config check`, which only *warns*). A
config under `/etc/ductile` fails to start: `symlinks detected in config paths but not allowed`.

So site config/state/logs on a **real** path — `/opt/ductile/{etc,var,log,plugins}` (`/opt` is a
real dir; the same reason Homebrew uses `/usr/local/etc`). **Do not** paper over it with
`service.allow_symlinks: true` — that weakens the guard on the very deployment whose purpose is the
wall. The binary stays at `/usr/local/bin/ductile` (on PATH for the CLI; not a config path).

### 10.3 Install the package layer

```bash
sudo BIN_SRC=/path/to/ductile bash deploy/install-macos.sh
```

Creates hidden, nologin worker accounts `_ductile-default`(1001)/`_ductile-untrusted`(1002) via
`dscl` (the macOS analog of `sysusers.d`), lays the `/opt/ductile` skeleton (root:wheel — `etc`
0700, `var` 0711 + per-worker `0700` dirs, `plugins` world r-x, `log`), installs the binary 0755
(never setuid) and the root LaunchDaemon plist (root:wheel 0644). Override `DEFAULT_UID` /
`UNTRUSTED_UID` to dodge a uid collision (the installer refuses to co-opt an in-use uid).

### 10.4 Place operator data

Plugin code → `/opt/ductile/plugins/<name>/` (root-owned, world r-x). Secret-zero (age key) →
`/opt/ductile/etc/secret/age.key` (0600 root). Config → `/opt/ductile/etc/config.yaml`:

```yaml
service:
  name: ductile-mac
  log_format: json
plugin_roots:
  - /opt/ductile/plugins
api:
  enabled: false          # or enable with vault-backed tokens — see DEPLOYMENT.md
state:
  path: /opt/ductile/var/state.db
accounts:
  default:                # REQUIRED — see the warning below
    uid: 1001
    gid: 1001
    state_dir: /opt/ductile/var/accounts/default
  untrusted:
    uid: 1002
    gid: 1002
    state_dir: /opt/ductile/var/accounts/untrusted
plugins:
  my_plugin:
    enabled: true
    run_as: untrusted     # grant: this plugin drops to the untrusted worker
```

> **⚠ Always define a `default` account.** A plugin with no `run_as` grant falls back to the
> `default` tier. If you omit `default`, ungranted plugins run at the **gateway uid — which is root
> on macOS**. The gateway warns loudly (`WARN: no 'default' account tier configured — ungranted
> plugins will run UNCONFINED`), but the safe wall is the one you configure, not the one you assume.

### 10.5 Load and verify the wall

```bash
sudo launchctl bootstrap system /Library/LaunchDaemons/com.mattjoyce.ductile.plist
tail -f /opt/ductile/log/ductile.log     # expect: "privsep enforcing: plugins drop to their resolved account"
```

Independent wall-bite check (no gateway needed) — the worker can't even traverse the 0700 config dir:

```bash
sudo -u _ductile-untrusted cat /opt/ductile/etc/secret/age.key
# cat: /opt/ductile/etc/secret/age.key: Permission denied
```

A confined plugin run shows `uid=NNNN(_ductile-<tier>)` and `Permission denied` on gateway secrets.
Stop the daemon with `sudo launchctl bootout system/com.mattjoyce.ductile`.

### 10.6 Enforced-mode gotchas

- **`sys_exec` (and peers) shell out *without* a shell.** `config.command` is `shlex.split` + `exec`
  — no redirects, pipes, `;`, or `$?`. Wrap shell scripts as `sh -c '…'` (one arg after the split).
- **Coexistence.** This enforced daemon (Label `com.mattjoyce.ductile`, **system** domain) is fully
  separate from the dev LaunchAgent (`com.mattjoyce.ductile-local`, **gui** domain) — different
  label; keep the API off or on a distinct `listen` so the two never clash.
- **Confined ≠ TCC-granted.** Confined data plugins should operate within `/opt/ductile` and explicit
  job inputs, not the operator's TCC-protected dirs (`~/Documents`, `/Volumes/…`). Reaching the
  operator's files/creds is the *credentialed* (trusted) tier, a deliberate opt-in — see
  [DEPLOYMENT_POSTURES.md](DEPLOYMENT_POSTURES.md), not this confined path.

---

## Known Differences from Linux Deployment

1. **No `~` expansion in config YAML** — `plugin_roots` and `state.path` do not expand `~`. Use absolute paths (e.g. `/Users/mattjoyce/...`).
2. **No EnvironmentFile equivalent** — launchd plist `EnvironmentVariables` replaces systemd's `EnvironmentFile`. Secrets must be inlined or loaded by the process at runtime.
3. **launchd owns PATH** — plugins that shell out (e.g. `sys_exec`) inherit only the PATH set in the plist, not your shell's PATH. Add Homebrew (`/opt/homebrew/bin`) explicitly.
4. **`strict_mode: false` recommended initially** — On first install, strict mode will reject config files with warnings. Disable until the config is stable, then re-enable.
5. **TCC resets on every rebuild** — ad-hoc signed binaries change cdhash on every build, invalidating TCC Files-and-Folders grants. See section 9.1 for the required pre-warm step. Linux has no equivalent.
6. **Enforced mode runs as root, not cap-only** (Section 10) — launchd has no `CAP_SETUID` equivalent, so the privsep gateway is a root `LaunchDaemon` (Linux runs non-root + two caps). Site config under `/opt/ductile`, never `/etc`/`/var` (those are symlinks, which the runtime config-path guard refuses).

---

## See Also

- [DEPLOYMENT.md](DEPLOYMENT.md) — Linux/systemd reference deployment
- [DEPLOYMENT_POSTURES.md](DEPLOYMENT_POSTURES.md) — unconfined / full-privsep / hybrid trust-tier chooser (Section 10 above is the enforced posture on macOS)
- [GETTING_STARTED.md](GETTING_STARTED.md) — Quickstart with the echo plugin
- [CONFIG_REFERENCE.md](CONFIG_REFERENCE.md) — Full config schema reference
- [OPERATOR_GUIDE.md](OPERATOR_GUIDE.md) — Day-2 operations, monitoring, maintenance
