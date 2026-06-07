# `_template` — the golden-path ductile plugin

Copy this directory, rename it, and fill in `handle()`. It is the **blessed
default pattern**: pure standard library, nothing fetched at spawn, structured
protocol-v2 I/O, and it writes only under its own account `state_dir`. It
already passes discovery and `test_run.py` unchanged — start from green.

```
_template/
  run.py          # the plugin — fill in handle(); honours the runtime contract
  manifest.yaml   # commands, I/O, config keys
  test_run.py     # subprocess tests proving the contract (copy + adapt)
  _response.py    # vendored from plugins/_lib/ — response builders
  _stopwatch.py   # vendored from plugins/_lib/ — sub-span timing
  _coerce.py      # vendored from plugins/_lib/ — config value coercion
```

## The confined-plugin runtime contract

Under privsep enforce the gateway drops the plugin to an unprivileged account
uid and roots its runtime at that account's own `0700` `state_dir`
(`docs/adr/confined-plugin-runtime-contract.md`). **You may rely on:**

- `cwd`, `$HOME`, `$XDG_CACHE_HOME` all `==` your `state_dir` — writable,
  private. Write state with relative paths. That is the whole storage story.
- Secrets in `request["secrets"]` — never the environment, never argv.
- A writable, shared `/tmp`.

**You must not** (each fails closed under enforce): write outside `state_dir`
(or `/tmp`); read `$HOME` dotfiles, `/home/<user>`, or ambient host
credentials; or fetch dependencies at spawn.

## Which tier is my plugin?

| Tier | Use when | Pattern |
|---|---|---|
| **1 — stdlib (this template)** | the default for anything with real logic or structured events | `#!/usr/bin/env python3`, stdlib only, vendor `_lib/` helpers |
| **2 — `sys_exec`** | the job genuinely *is* "run a stable system command" (build, sync, scheduled maintenance) and exit-code/output is enough | the bundled `sys_exec` plugin — no bespoke code |
| **3 — fetch-at-spawn (advanced)** | a third-party library is unavoidable and cannot be vendored/pre-built | `uv run --script` (py) or a **pre-built/bundled** node artifact; per-account cache, cold resolve per spawn — document the isolation caveat |

Prefer Tier 1. Reach for Tier 3 only when you must, and prefer **building
ahead of time** (vendor deps / bundle a single artifact into the read-only
plugin dir) over fetching at spawn.

> **Node / TypeScript:** the contract is runtime-neutral (npm cache → `$HOME/.npm`,
> `node_modules` → cwd, ts-node/tsx cache → `$XDG_CACHE_HOME` all land under
> `state_dir`). But `npm install` at spawn runs registry `postinstall` scripts on
> every invocation — a live supply-chain surface. **Build ahead:** `esbuild`/`ncc`/
> `bun build --compile` a single artifact (or vendor `node_modules`) into the
> root-owned, read-only `/opt/ductile/plugins/<name>/`, and run it fetching nothing.

## Operator wiring (config.yaml)

The plugin author declares *needs*; the operator grants *privilege* (privsep
ADR §4). For a confined, vault-using instance:

```yaml
accounts:
  default: { uid: 1001, gid: 1001, state_dir: /var/lib/ductile/accounts/default }

plugins:
  my_plugin:
    enabled: true
    run_as: default            # privsep: drop to this account's uid/gid/state_dir
    requires_vault: true       # fail CLOSED if no secret is delivered
    vault_principal: my-plugin # kebab principal authorizing the secret(s)
    config:
      max_records: 1000
```

The plugin then reads its secret from `request["secrets"]["api_token"]`. Each
distinct route/instance that needs a secret needs its own `vault_principal` +
grant.

## Develop

```bash
chmod +x run.py                 # discovery requires an executable entrypoint
python -m pytest test_run.py    # green out of the box
echo '{"command":"health"}' | ./run.py
```
