---
title: Filesystem layout for the v1.0 prod release
status: Accepted
created: 2026-06-07
accepted: 2026-06-07
tags: [adr, deployment, privsep, fhs, packaging, v1.0]
relates_to: ["PrivSec and Secrets ADR (§3 Layer 1b, §8, §11)", "kanban #83 privsep epic", "kanban #103 phase-2", "docs/runbooks/privsep-thinkpad-enforce.md"]
---

# ADR: Filesystem layout for the v1.0 prod release

## Status

Accepted (2026-06-07). Validated by the first live privsep-enforce deployment on the Thinkpad
(2026-06-07): the layout below is what made the uid drop physically possible.

## Context

ductile is a long-running daemon that, under privsep (PrivSec ADR Layer 1b), runs as an unprivileged
**gateway** service user holding only `CAP_SETUID`+`CAP_SETGID`, and drops each plugin to a distinct
unprivileged **account** uid at spawn. For that wall to exist, the filesystem must cooperate: the
gateway user, the account users, and root each need different, non-overlapping access to the binary,
config, secrets, state, and plugin code.

The homelab grew up as a **`$HOME`-rooted install** — binary in `~/.local/bin`, config in
`~/.config/ductile`, plugin code in `~/Projects/*`, secrets in `~/.config/secrets/*`. That layout made
privsep **impossible to retrofit**: a system service running as a non-`matt` user cannot traverse
`/home/matt` (`0700`), so it could read neither the plugin code nor the secret files — the gateway
boots *empty*, not degraded. Confirmed live (kanban #83). The fix was a clean **deploy-as-new** into
FHS paths, not a migration.

This ADR fixes the canonical v1.0 footprint so the privilege model and the directory model are the
same model.

## Decision

**v1.0 ships as an FHS system package.** Files are separated along two axes simultaneously —
**mutability** (immutable code/config vs. churning state) and **privilege** (root / gateway / account
/ world) — such that **every directory boundary is also a uid+permission boundary**. Nothing the
service depends on lives under `/home`.

### Precedent

This is a solved problem; we copy the daemons that hold secrets and drop privilege.

| Concern | postgres | nginx | sshd | **ductile v1.0** |
|---|---|---|---|---|
| binary | `/usr/lib/postgresql/*/bin` | `/usr/sbin/nginx` | `/usr/sbin/sshd` | `/usr/bin/ductile` — never setuid |
| config | `/etc/postgresql` | `/etc/nginx` | `/etc/ssh` | `/etc/ductile` |
| secret-zero | — | `/etc/ssl/private` 0600 | `/etc/ssh/ssh_host_*` 0600 | `/etc/ductile/secret/age.key` 0600 |
| mutable state | `/var/lib/postgresql` | `/var/cache/nginx` | — | `/var/lib/ductile` |
| runtime | `/run/postgresql` | `/run/nginx.pid` | `/run/sshd.pid` | `/run/ductile` |
| logs | journald | `/var/log/nginx` | journald | journald |
| privilege model | postmaster + backends | master root + unpriv workers | priv-sep child | gateway (caps) + per-plugin account uids |

ductile is the nginx/sshd privilege-separation model crossed with postgres's "a stable service user
owns a persistent data dir."

### Canonical Linux layout

| Path | Purpose | owner:group | mode | Rationale |
|---|---|---|---|---|
| `/usr/bin/ductile` | binary | `root:root` | `0755` | immutable; runnable as an unprivileged **utility** (`config validate`, `secrets keygen`); **never setuid** — privilege is init-conferred, not file-borne (PrivSec §11) |
| `/etc/ductile/` | admin-authored config (`config.yaml`, includes, `.checksums`) | `ductile:ductile` | `0750` | gateway reads; **plugin accounts cannot traverse in**; admin edits via sudo |
| `/etc/ductile/secret/age.key` | **secret-zero** (age key) | `ductile:ductile` | `0600` | sshd `/etc/ssh` co-location pattern; readable by the (unprivileged) gateway, by no one else. **Backup-safety is an enforced invariant, see below** |
| `/var/lib/ductile/` | mutable state root | `ductile:ductile` | `0700` | postgres-`PGDATA` pattern |
| `/var/lib/ductile/ductile.db` | SQLite state | `ductile:ductile` | `0600` | gateway-private |
| `/var/lib/ductile/vault.age` | encrypted secret store | `ductile:ductile` | `0600` | the vault is mutable **state** (daemon sole-writer; secrets added/rolled at runtime) → `/var/lib`, not `/etc` |
| `/var/lib/ductile/accounts/<tier>/` | per-plugin scratch/state | `ductile-<tier>:…` | `0700` | each account owns its own — **this boundary is the wall** (cross-account = EACCES) |
| `/opt/ductile/plugins/` | plugin code (polyglot add-ons) | `root:root` | `0755` (world r-x) | code is a **root-owned immutable add-on**: a popped plugin can read+exec but cannot rewrite what it runs; `/opt` is FHS's home for self-contained add-on software |
| `/run/ductile/` | pid, sockets | `ductile:ductile` | `0700` | ephemeral (tmpfs); pid/lock belongs neither in config nor state |
| logs | journald | — | — | `StandardOutput=journal`; no logfiles unless an explicit sink is required |
| `/var/backups/ductile/` | snapshots | operator | `0700` | **outside `/var/lib`** — never nest backups in the state they capture; key custody stays separate |

### Provisioning

Shipped *with* the package, never done at runtime: `sysusers.d` creates `ductile` +
`ductile-default` + `ductile-untrusted`; `tmpfiles.d` creates the directories with the ownership
above; the systemd unit runs `User=ductile` + `AmbientCapabilities=CAP_SETUID CAP_SETGID`. The daemon
**never `useradd`s** (the nginx/postgres package pattern).

### The three load-bearing opinions

1. **Plugin code in `/opt`, root-owned, world-r-x — never under `$HOME`, never writable by the gateway
   or the accounts.** A compromised plugin cannot tamper with sibling code or its own next run, and
   attestation fingerprints immutable bytes.
2. **`vault.age` is state (`/var/lib`); `/etc` is static.** A runtime-mutated, daemon-sole-written
   file does not belong in `/etc`; the split keeps `/etc/ductile` reviewable/lockable and `/var/lib`
   as the churn zone.
3. **Secret-zero lives at `/etc/ductile/secret/age.key 0600` (sshd-style co-location), and its
   backup-safety is a *tested invariant*, not a comment.** See below.

### Secret-zero placement (the age key) — resolved

sshd keeps `ssh_host_*_key` *inside* `/etc/ssh` at `0600`; the control is permissions, not location.
We adopt the same: `/etc/ductile/secret/age.key`, `0600`, `ductile`-owned (the gateway is
unprivileged, so unlike root-run sshd it must be owner-readable; plugin accounts can't reach it
because `/etc/ductile` is `0750`).

The one ductile-specific hazard: `ductile system backup --scope config` archives the config tree into
a **portable** bundle, and age-at-rest's value is "a leaked bundle is useless without the separately
custodied key." Co-locating the key therefore makes secret-zero's backup-safety depend on the
backup tool's exclude rule. We accept co-location **only because that exclusion is promoted to an
enforced invariant**: a regression test builds a `--scope config` archive and asserts that
`secret/` / the age key is absent. (PrivSec §11 principle: a control's integrity must live with its
enforcer — so the exclusion is code, not a glob nobody re-checks.) Absent that test, the key must move
to a sibling outside the archived tree (e.g. systemd `LoadCredential` / `/etc/credstore.encrypted`).

### macOS mapping (homelab spans both)

No FHS, but the four boundaries map: binary `/usr/local/bin/ductile`; config `/Library/Application
Support/ductile`; state `/usr/local/var/ductile`; plugin code `/usr/local/lib/ductile/plugins`
(root-owned); age key in the **Keychain** or a root `0600` file; service in `/Library/LaunchDaemons`
(root — launchd has no cap-only model, so macOS enforce runs the daemon as root and drops).

## Consequences

**Positive**
- The privilege model and the directory model are one model; the privsep wall is buildable by
  construction (validated live, Thinkpad 2026-06-07).
- A popped plugin cannot rewrite plugin code, read the config/secret surface, or reach a sibling's
  state — each is a different uid/permission domain.
- Standard ops tooling (systemd, sysusers/tmpfiles, journald, packaging) applies directly.

**Negative / costs**
- A real migration off the `$HOME`-rooted homelab install (deploy-as-new; see runbook). Admin
  automation that genuinely needs privilege (docker/apt/`fabric`) does **not** fit this footprint and
  must run as a separate, unconfined service with its own working area (kanban #103).
- The backup-exclusion of secret-zero is **load-bearing** and must stay tested (see above), or it
  silently rots.

**Enforced invariants (tests that must exist)**
- `--scope config` backup archive never contains `secret/` or the age key.
- Boot fails closed if `/etc/ductile` is not gateway-owned or an account `state_dir` is not
  account-owned (already enforced by the privsep boot reconcile).
