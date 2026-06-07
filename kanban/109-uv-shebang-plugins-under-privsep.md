---
id: 109
status: todo
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
