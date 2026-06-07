# Runbook — first live privsep ENFORCE cutover (Thinkpad, Linux)

> Status: first live enforce deployment (epic #83, the "one host → observe → next" step).
> Linux/cap-only path (proven on the Dell). Run **on the Thinkpad**. Reversible — keep the old
> service + binary until the wall-bite check (step 7) passes.

## What changes (read first)

The Thinkpad runs ductile today as a **`--user` LaunchAgent/systemd-user service** as your own uid
(hygiene-only, `unconfined`). Enforce requires a **system service running as the `ductile` account
with `CAP_SETUID`+`CAP_SETGID`**, dropping each plugin to an account uid. That means the **config,
state, vault, and age key move to ductile-owned system paths** — the `ductile` user (uid 984) cannot
read files under your `$HOME` (`0700`). New topology (from `deploy/systemd/`):

| | today (`--user`) | enforce (system) |
|---|---|---|
| runs as | your uid | `ductile` (984) + CAP_SETUID/SETGID |
| config | `~/.config/ductile` (or `~/admin/ductile-local`) | `/etc/ductile` (ductile-owned `0700`) |
| state | under the above | `/var/lib/ductile` |
| age key | `~/.config/secrets/…` | inside `/etc/ductile`, ductile-owned `0600` |
| plugins | grants → ungranted = your uid | `sys_exec → untrusted`; others → `default` (uid 1001) |

The boot fs-reconcile tightens the config dir to `0700` ductile-owned (accounts can't read it) and
verifies each account's `0700` state_dir — **fail-closed**: a misconfig refuses to boot, it does not
half-confine.

---

## 0. Recon (on the Thinkpad — paste the output back to finalize per-plugin fixups)

```bash
cd /path/to/ductile && git fetch && git checkout feat/privsep-uid-separation && git pull
CFG=$HOME/.config/ductile           # adjust if yours differs (e.g. ~/admin/ductile-local/config)
ductile config check --config "$CFG" --json | tee /tmp/ductile-preflight.json
# Enabled plugins + any operator-owned paths they read/write (these are the EACCES candidates):
grep -nE '^[a-zA-Z0-9_-]+:|enabled:|run_as:|allowed_(read|write)_paths:|output_dir:|root:|path:|working_dir:|command:' "$CFG/plugins.yaml"
ls -la "$CFG"                        # note where config/state/vault/age key live + current owner
systemctl --user status ductile-local 2>/dev/null | head -3 || launchctl list | grep ductile
```

**Per-plugin rule:** under enforce a plugin runs as its account uid → it can only write its own
`state_dir`, read world-readable code, and get secrets over stdin. Known breakage classes from the
audit: `file_handler` (writes `allowed_write_paths`), `file_watch`/`folder_watch` (read watched
paths), `sys_exec` (→ `untrusted`), and **any plugin reading a secret from a `$HOME` dotfile** (e.g.
`fabric` → `~/.config/fabric/.env`) → move that secret into the vault or copy it into the account's
state_dir. Per your risk call, it's also fine to cut over and read the EACCES failures out of the job
logs, then fix the few that break.

## 1. Rollback point (do not skip)

```bash
ductile system backup --to ~/backups/pre-privsep-$(date -u +%Y%m%dT%H%M%SZ).tar.gz --scope config --config "$CFG"
cp "$(command -v ductile)" ~/backups/ductile-prev        # keep the current (hygiene-only) binary
cp -a "$CFG" ~/backups/ductile-config-snapshot           # plain copy of config+vault for hand-revert
```

## 2. Build + stage the branch binary (don't install yet)

```bash
cd /path/to/ductile && go build -o ~/staging/ductile-new ./cmd/ductile
~/staging/ductile-new --version                          # confirm it's the privsep branch build
```

## 3. Provision accounts as root (the deploy/systemd/ templates)

```bash
sudo install -m0644 deploy/systemd/ductile-accounts.sysusers.conf /etc/sysusers.d/ductile-accounts.conf
sudo install -m0644 deploy/systemd/ductile-accounts.tmpfiles.conf /etc/tmpfiles.d/ductile-accounts.conf
sudo systemd-sysusers                                    # creates ductile(984) default(1001) untrusted(1002)
sudo systemd-tmpfiles --create /etc/tmpfiles.d/ductile-accounts.conf   # creates the 0700 state dirs
id ductile ductile-default ductile-untrusted             # verify they exist
```

## 4. Relocate config + secrets to ductile-owned system paths

```bash
sudo mkdir -p /etc/ductile
sudo cp -a "$CFG"/. /etc/ductile/                        # config.yaml, plugins.yaml, pipelines.yaml, vault.age, .checksums, ductile.db
sudo cp -a ~/.config/secrets/ductile/age.key /etc/ductile/age.key   # key moves INTO the (soon 0700) config dir
sudo chown -R ductile:ductile /etc/ductile /var/lib/ductile
sudo chmod 600 /etc/ductile/age.key
# Edit /etc/ductile/config.yaml as root:
#   state.path: /var/lib/ductile/ductile.db   (or keep under /etc/ductile if you copied the DB there)
#   secrets.age_key_file: /etc/ductile/age.key
#   accounts:
#     default:   { uid: 1001, gid: 1001, state_dir: /var/lib/ductile/accounts/default }
#     untrusted: { uid: 1002, gid: 1002, state_dir: /var/lib/ductile/accounts/untrusted }
#   plugins.sys_exec.run_as: untrusted        # ungranted plugins fall back to default
sudo -u ductile /usr/local/bin/ductile config check --config /etc/ductile/config.yaml   # MUST be clean
```

## 5. Per-plugin fs fixups (from step 0 / first-boot EACCES)

For each plugin that reads/writes an operator-owned path: repoint it under `/var/lib/ductile/accounts/<tier>/…`
(owned by the account), make the input world-readable, or move its secret into the vault. `sys_exec`'s
`working_dir` must be owned by `ductile-untrusted`.

## 6. Cutover

```bash
sudo cp ~/staging/ductile-new /usr/local/bin/ductile
systemctl --user disable --now ductile-local 2>/dev/null || launchctl bootout gui/$(id -u)/com.mattjoyce.ductile-local
sudo install -m0644 deploy/systemd/ductile.service /etc/systemd/system/ductile.service
sudo systemctl daemon-reload && sudo systemctl enable --now ductile
```

## 7. VERIFY — including the wall-bite check (the part revert can't save you from)

```bash
journalctl -u ductile -n 60        # expect: "privsep enforcing: plugins drop to their resolved account"
                                   # NOT "privsep boot gate ... refusing", NOT "privsep UNCONFINED"
curl -s localhost:8081/healthz     # plugins_loaded == pre-deploy baseline
# run one job per enabled plugin; watch job logs for EACCES / "account uid/gid drop failed"

# WALL-BITE — prove the wall physically bites (availability revert won't catch a silent no-wall):
sudo -u ductile-default cat /etc/ductile/age.key        # MUST print: Permission denied
sudo -u ductile-default ls /var/lib/ductile/accounts/untrusted   # MUST print: Permission denied
# (optional, the headline threat) cross-uid ptrace between two plugins on DIFFERENT accounts must fail.
```

If the age key is readable by `ductile-default`, the wall is NOT real — stop and fix ownership before
trusting it.

## 8. Rollback (if it refuses to boot or breaks plugins you can't quickly fix)

```bash
sudo systemctl disable --now ductile
sudo cp ~/backups/ductile-prev "$(command -v ductile)"
systemctl --user enable --now ductile-local              # or launchctl bootstrap the old LaunchAgent
# config/vault are untouched at $CFG; the /etc/ductile copy is additive and harmless to the old service.
```

---

## Notes
- This is the first live enforce host. Observe it before the macOS rollout (#95) — and remember the
  Mac needs a *different* shape (root LaunchDaemon, no cap-only; see #102).
- The age key now lives inside the `0700` ductile-owned config dir, so accounts can't read it. If you
  prefer it outside the bundle (ADR §8 floor), keep it root-owned `0600` at a path `ductile` can read
  and point `secrets.age_key_file` there — just confirm the wall-bite check still denies accounts.

## Learnings from the first live run (2026-06-07) — VALIDATED

This runbook was executed live on the Thinkpad; the wall is proven (enforce + uid drop to 1002 +
cross-account EACCES). What we learned:

- **Deploy-as-NEW, don't migrate.** The live config was too home-entangled to re-home (plugin code
  under `~/Projects/*`, ~12 secret `.env` under `~/.config/secrets/*`, config under `~/.config`). A
  fresh `/etc/ductile` + `/opt` code + minimal confinable config booted enforce green in minutes; the
  migration would have been multi-hour with whole-gateway breakage.
- **Footgun (costs a fail-closed boot refusal):** `chown -R ductile:ductile /var/lib/ductile` clobbers
  the account-owned state dirs and trips the boot gate. chown **only** `/etc/ductile`; `tmpfiles.d`
  owns the account dirs — leave them.
- **EMPTY DB for a newer binary.** Carrying the old prod DB risks a schema-validation boot refusal vs a
  newer binary; a fresh DB sidesteps it entirely (history stays in the backup).
- **Wall-proof that worked:** `sys_exec(command=id)` via API → stdout `uid=1002(ductile-untrusted)`
  with groups reset to 1002 only; `sudo -u ductile-default ls .../untrusted` → `Permission denied`.
- **DX:** config dir `0700` ductile-owned → `ductile job inspect` as a human user hits EACCES; read job
  results via the API or as the `ductile` user.
- **Confinable vs unconfinable split:** `docker compose` / apt scripts / `fabric`(`~/.config/fabric`) /
  `file_handler`(reads `/home/matt`) cannot run as an account uid — keep admin automation on an
  unconfined gateway. Restoring the confinable plugins enforced is Phase 2 (card #103).

### Phase 2 learnings (2026-06-07, live)

- **`plugin lock` is a REQUIRED migration step, not an end-cap.** Relocating plugin code to `/opt`
  changes its bytes' provenance → no recorded fingerprint → privsep's #93 binding refuses to honor the
  `default` grant and **fails SAFE, downgrading the plugin to the least-priv tier (`untrusted` 1002)**.
  The plugins still run, dropped + confined, just least-priv. Run `ductile plugin lock --all` against
  the `/opt` tree to record fingerprints, then they land on their intended `default` (1001). Witnessed
  live: youtube_playlist + check_db_garmin both downgraded→untrusted until locked. (This is a clean
  in-the-wild proof of the #93 swap-defence.)
- **Plugin secrets are NOT a config reconcile.** `PluginConf` has no nested `secret_ref`; plugin secret
  delivery is either a `<field>_ref:` key-suffix (loader substitution — unverified for plugins) or the
  spawn-time stdin `secrets` map (principal == plugin name, **kebab-case only**, and the plugin must
  read that map). Snake-case plugin names are rejected as principals (tested: 400). So secret-holding
  plugins need a dedicated vault-native workstream (card #107) — only **keyless** plugins restore via
  config alone. Worse, the vault compose path is fail-OPEN on an unknown principal (card #108) — the one
  non-fail-closed seam in an otherwise fail-closed design.
- **`migrate everything` ≠ everything enforced.** The estate splits three ways: keyless-confinable
  (enforced now), secret-holding-confinable (enforced after #107), and unconfinable admin/docker
  (a second *unconfined* instance, card #106). The enforced gateway is the data plane, not the whole world.

### Redeploy / binary refresh (validated live 2026-06-07)

The repeatable way to push a new binary (validated on the live Thinkpad):

```bash
cd /path/to/ductile && git pull --ff-only && go build -o ~/staging/ductile-new ./cmd/ductile
sudo BIN_SRC=~/staging/ductile-new bash deploy/install.sh   # idempotent FHS layer (ADR)
sudo systemctl restart ductile
sudo -u ductile ductile config check --config /etc/ductile/config.yaml   # VALID
journalctl -u ductile -n 30 --no-pager                                   # "privsep enforcing"
# verify a job still drops to its account (live `ps` shows uid 1001/1002), wall-bite holds
```

- **`deploy/install.sh` is safe to re-run:** idempotent; lays/asserts only the FHS *structure* + binary +
  unit; it does NOT touch config/vault/plugin code and does NOT chown `/var/lib/ductile` (no account-dir
  clobber). Confirmed live: created `/run/ductile`, account dirs intact, new binary `root:root 0755`.
- **A binary swap needs NO re-lock** — plugin fingerprints are of the plugin code keyed by the vault
  nonce, not the gateway binary; `config lock`/`plugin lock` only matter when config/plugin *bytes* change.
- **#101 verified live:** `config check` on a config with `accounts:` removed but secrets kept fires
  `config is VALID but privsep is UNCONFINED … a popped plugin can read the decrypted secrets`. And a
  `run_as` with no `accounts:` is a hard config-load error (fail-closed, not per-job).
- **#108 verified by unit tests:** `plugins.<name>.requires_vault: true` makes an unknown principal fail
  the spawn closed (live demo skipped — it would break a plugin).
