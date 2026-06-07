---
id: 109
status: doing
priority: High
blocked_by: []
tags: [privsep, plugins, packaging, uv, enforce]
---

# uv-shebang plugins fail under privsep enforce (exit 127)

> **Nav:** [[83-privsep-epic]] · [[107-plugins-vault-native-conformance]] · found 2026-06-07 while
> making github_repo_sync vault-native. The secret path works; the **runtime** doesn't under enforce.

**Job story:** *When* a plugin uses a `uv run --script` shebang (inline deps), *I want* it to run
dropped to an account uid, *so* git/uv-based integrations can be enforced like the plain-python3 ones.

## The finding (ductile-admin, 2026-06-07)

`github_repo_sync` (and the other git plugins) is **vault-native and ready** (reads `secrets[token_secret]`,
principal + grant in place, follows the #107 recipe) but **fails to RUN under enforce — exit 127**:
- The shebang is `uv run --script` (uv resolves inline script deps). `uv` lives at
  `/home/matt/.local/bin` → **unreachable** to an account uid (HOME is `0700`).
- Even a **system-installed** `uv` fails: uv walks its config **UP the directory tree** into the
  `0700` `/var/lib/ductile` cwd it can't read, and needs a **writable cache + HOME** the unprivileged
  account doesn't have.
- Plain `python3` plugins are fine; only the `uv`-shebang ones break.

**Affected (all `uv`-shebang):** `github_repo_sync`, `git_repo_sync`, `git_commit_push`, `repo_policy`.
Currently **disabled** on the enforced gateway (gateway healthy, 0 circuit-open).

## Fix options (decide on pickup)
- **(a) Make uv account-friendly:** system-install `uv` (on PATH for the account); set per-account
  `UV_CACHE_DIR` + `HOME` to writable account-owned dirs (under the account `state_dir`); set the
  plugin spawn cwd OFF the `0700` `/var/lib/ductile` (so uv's up-tree config walk doesn't hit it).
  Possibly pass `UV_*` via `service.plugin_env_passthrough` / per-plugin env.
- **(b) De-uv the git plugins:** convert the four to a plain `python3` shebang with system-installed
  deps (no inline-dep resolution at spawn). Simplest for privsep; loses uv's self-contained deps.

Recommend (b) for the git plugins if their deps are small/stable; (a) if uv's ergonomics are wanted
fleet-wide. Either way: the spawn-cwd-into-0700 interaction is worth fixing generally (a plugin's cwd
should be its account-owned dir, not the gateway's `0700` state root).

## Acceptance
The git/uv plugins run enforced (dropped to their account uid) and complete a real job — no exit 127,
no reach into `/home` or the gateway's `0700` cwd.

## Narrative
- 2026-06-07: Found during the #107 vault-native push — github_repo_sync's secret path was done but it
  exit-127'd under enforce because its `uv run --script` shebang needs uv on PATH + a writable HOME/cache
  and trips on uv's up-tree config walk into the `0700` cwd. A privsep×packaging gotcha distinct from
  secrets: plain-python3 plugins enforce fine, uv-shebang ones don't. (by @assistant)

## REFINED DIAGNOSIS + FIX (2026-06-07, on-box repro) — root is broader than uv

**exit-127 is already fixed** (a system `uv` now at /usr/local/bin — dropped account finds it on PATH).
The live failure is **exit 2, and it's a privsep PLATFORM gap, not a plugin bug:**

A confined plugin is spawned with **HOME and cwd = the gateway's `/var/lib/ductile` (0700 ductile)** —
so it has **no writable HOME** (uv's `$HOME/.cache` → permission denied) and an unreadable cwd. Worse:
the account's OWN state_dir (`/var/lib/ductile/accounts/<a>`, 0700/account-owned) is **UNREACHABLE** —
the `0700` on the parent `/var/lib/ductile` blocks the account uid from traversing to it. Repro:
`sudo -u ductile-default cd /var/lib/ductile/accounts/default` → Permission denied. And `cmd.Dir`
can't save it (Go does the setuid drop BEFORE chdir, so the chdir runs as the account + hits the same
wall). **The only writable abs path a dropped account has today is host `/tmp`.**

**This affects EVERY plugin, not just uv** — proof: `github_repo_sync/run.py:116`
`clone_root.mkdir(/var/lib/ductile/accounts/default/github)` is a state_dir write that face-plants on
the same wall. So the per-account `state_dir` feature is effectively half-dead (earlier "writes"
were event emits, which never touched it).

### The real frame: privsep silently changed the plugin RUNTIME CONTRACT
Pre-enforce a plugin had a writable HOME, a usable cwd, and /tmp. Under enforce it has none. Every
runtime (uv, pip, node, go-build, playwright, even `__pycache__`) assumes a writable HOME+TMPDIR, so
they break one-by-one. The fix is not per-tool — **define + guarantee a confined-plugin runtime
contract:** every dropped plugin gets (1) a writable private HOME, (2) a writable TMPDIR, (3) its own
writable cwd (its state_dir), (4) secrets over stdin, and NOTHING ambient from the host. (Worth an ADR
+ a "plugin runtime contract" doc.)

### FIX = C, two parts (gateway-side, MacM1)
1. **tmpfiles:** `/var/lib/ductile` + `/var/lib/ductile/accounts` → **0711** (traverse-only, not
   listable; per-account dirs STAY `0700`/account-owned — the /home pattern). Declarative, survives
   reboot. Makes state_dirs reachable. (vault.age/ductile.db stay `0600` → contents safe; only a minor
   stat-of-size leak, acceptable for the popped-plugin threat.)
2. **gateway confined spawn:** set `HOME` + `XDG_CACHE_HOME` (+ cwd) = the account's `state_dir`.
   Env-vars sidestep the setuid-before-chdir ordering (the plugin uses $HOME at runtime as its uid).

On-box verification owned by ThinkPad-ductile-admin: (a) 0711 does NOT trip the boot fs-reconcile gate;
(b) vault.age + ductile.db stay unreadable to accounts; (c) a confined plugin can write its state_dir;
(d) cross-account isolation still bites (default can't read untrusted's dir).

### Decisions
- **A (shared uv cache via systemd env): REJECTED** — a cache writable by multiple account uids is a
  cross-account poisoning vector (popped `untrusted` poisons → `default` executes).
- **B (stdlib rewrite): the fallback + a norm.** Revised for github_repo_sync = urllib swap (zero deps)
  **+ DROP the spurious `clone_root.mkdir`** (it pre-creates a dir for downstream git_repo_sync — a
  downstream responsibility leaking upward; this discovery plugin only emits events). → works TODAY,
  no dependency on C. ThinkPad doing it on-box. The git_* / repo_* downstream plugins (write/clone +
  uv) stay parked behind C.
- **Design norm:** plugins prefer stdlib/system runtimes + assume ONLY the runtime contract above —
  never $HOME dotfiles, /home paths, or ambient creds.

## C IMPLEMENTED (MacM1, 2026-06-07) — gateway-side, on `feat/privsep-uid-separation`
Both parts landed + green locally (build, gofmt, `go test ./internal/dispatch -p 1`; the one failure
is the known parallel-race flake `TestSpawnPluginTimeoutKillsProcessGroup`, passes 3/3 in isolation,
unrelated — unconfined `config.Defaults()` + absolute pid path).
1. **tmpfiles** `deploy/systemd/ductile-accounts.tmpfiles.conf`: `/var/lib/ductile` + `.../accounts`
   `0700`/`0755` → **`0711`**; per-account dirs stay `0700`. Verified gate-safe by reading
   `secret_surface.go` + live config: `State.Path` is the db **file** (`/var/lib/ductile/ductile.db`),
   so `reconcileSecretPath` tightens the file to `0600` and NEVER touches the dir mode → `0711` survives boot.
2. **gateway spawn** `internal/dispatch/`: new `withAccountRuntimeEnv` (`env.go`) drops inherited
   `HOME`/`XDG_CACHE_HOME` and re-points both at the account `state_dir`; `subprocess_executor.go` calls
   it + sets `cmd.Dir = state_dir` for confined accounts only (unconfined untouched). Tests added in
   `env_test.go` (rebases home+cache; no gateway-home leak).

Contract written up: `docs/adr/confined-plugin-runtime-contract.md` (the spec the exemplar rewrite follows).

### STILL OPEN
- **On-box 4-point verify** (ThinkPad-ductile-admin owns): (a) `0711` doesn't trip the boot fs-reconcile
  gate; (b) `vault.age`/`ductile.db` stay unreadable to accounts; (c) confined plugin writes its state_dir;
  (d) cross-account isolation still bites. Needs the new binary deployed.
- **Exemplar rewrite** (cross-repo `ductile-plugins`, war-room): stdlib-default / uv-advanced re-tier +
  responsibility-leak sweep, per the ADR. The git_*/repo_* downstream plugins unpark once (c) is verified.
