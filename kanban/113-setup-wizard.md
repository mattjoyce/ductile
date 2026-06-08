---
id: 113
status: idea
priority: Medium
tags: [ux, setup, cli, config, design]
---

# Setup Wizard — `ductile setup` text UI

A terminal wizard that walks a new user through initial configuration choices and writes valid config files. Constrained to text UI (no web, no GUI). Possibly `ductile setup` subcommand or standalone binary.

## Design lens: Kay × Victor

**Kay:** Don't recreate a config file editor with extra steps. The wizard must teach the ductile mental model — *events → pipelines → plugins* — and let config choices fall out of understanding that model. Computing as a medium of thought, not a paper form.

**Victor:** The user must SEE the config being built as they make choices. Show a partial YAML preview after each phase — not at the end. Choices have immediately visible consequences. The user touches the idea directly instead of imagining a file that will appear later.

## The minimal mental model question

Instead of "what listen address for the API?" open with:

> "Ductile connects things. Something triggers an event. A pipeline handles it. A plugin does the work. How will events reach ductile?"

The rest follows.

## Phases

### Phase 1 — Where to live
- Config directory: default `~/.config/ductile/` — one question
- Service name: default `ductile` — only ask if they intend multiple instances

### Phase 2 — How the outside world reaches in
Present as a single multi-select: *"How will events enter ductile?"*

```
[x] REST API     — other programs call it directly    (127.0.0.1:8081)
[x] Webhooks     — external services push to it       (127.0.0.1:8091)
[ ] Schedules only — nothing inbound, just timers
```

Follow-up only if selected: confirm or override each listen address. Victor point: show an ASCII topology alongside the selection:

```
[ external service ] ──webhook──▶ [ ductile ] ──▶ [ plugin ]
[ your script ]      ──api call──▶ [ ductile ] ──▶ [ pipeline ]
```

### Phase 3 — Credentials (automatic, not manual)
`REPLACE_WITH_ADMIN_TOKEN` is a trap. The wizard:
1. Generates a token using `crypto/rand`
2. Displays it **once** with "Save this — it won't be shown again"
3. Writes the hashed/opaque form to config

Single follow-up question: *"Do you want a read-only token too? (useful for monitoring tools)"*

No manual token entry. No placeholder strings in the output config.

### Phase 4 — Plugin discovery (not enumeration)
Scan `~/.config/ductile/plugins/` and `./plugins/` first. Then:

```
Found 3 plugins: echo, sys_exec, folder_watch
  echo          — emits a heartbeat event on a schedule
  sys_exec      — runs a shell command when triggered
  folder_watch  — watches a directory for file changes

Enable all? [Y/n] or choose individually
```

For each enabled plugin, ask **only the one required config value** (e.g., `sys_exec` needs a command; `folder_watch` needs a watch path). Don't enumerate optional settings.

Kay point: the one-line description is load-bearing. The user must understand what the plugin IS before they're asked to configure it. A plugin named `fabric` with no explanation is just a form field.

### Phase 5 — Live preview (after each phase)
After each phase completes, render the current partial YAML inline:

```yaml
# Current config (phase 2 of 5 complete):
service:
  name: ductile
api:
  enabled: true
  listen: 127.0.0.1:8081
webhooks:
  listen: 127.0.0.1:8091
# (tokens pending step 3, plugins pending step 4...)
```

This is Victor's requirement made concrete. The user is not filling out a form — they are watching a config file come into existence through their choices.

### Phase 6 — Verify
After writing files, run `ductile config validate` and show inline. On pass:

```
✓ Config valid
Start with: ductile system start
```

On failure: show exactly which field and offer to re-answer that specific question. Don't make the user re-run the whole wizard.

## Skip/default path
Press Enter on every question → minimal working config:
- Local API (127.0.0.1:8081), no webhooks, no plugins
- Generated admin token (displayed once)
- State at `~/.config/ductile/ductile.db`

A config that `ductile system start` will not reject. The floor.

## Output file structure
Wizard should match the multi-file split the existing example config uses:

```
~/.config/ductile/
  config.yaml        ← service + state + plugin_roots + includes
  api.yaml           ← api + tokens
  plugins.yaml       ← per-plugin config
  pipelines.yaml     ← empty or example
  webhooks.yaml      ← webhooks (if enabled)
```

Single-file mode (`--flat`) should also be supported for simplicity in testing/dev.

## Standalone vs. integrated

`ductile setup` as a subcommand is cleaner:
- Uses the same config loader to validate output immediately
- Users already know the binary
- No PATH juggling

Standalone binary only makes sense for bootstrap scenarios (wizard must run before ductile is fully installed). Not worth the split.

## Open questions
- Should the wizard support re-running (`ductile setup --update`) to add plugins to an existing config without clobbering it?
- How does wizard interact with `ductile config validate --whole` (the strict full-config check)?
- Should pipeline templates be offered at setup time? (e.g., "I want to summarize YouTube videos" → pre-wires a known pipeline)

## References
- Config schema: `schemas/config.schema.json`
- Example configs: `config/` directory
- Config loader: `cmd/ductile/config.go`, `cmd/ductile/config_manage.go`
