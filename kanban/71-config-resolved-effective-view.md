---
id: 71
status: done
priority: Normal
blocked_by: []
tags: [config, observability, hickey, hidden-state, decomplect]
---

### DONE (2026-06-06)
Added `config show --effective [plugin]` and `config get --effective <plugin>[.<field>]`. New
`config.EffectivePluginConf(raw, maxWorkers)` resolves each plugin field per *runtime* semantics
(`>0 ? explicit : default`, defaults single-sourced from `DefaultPluginConf`) and returns a
field→source map; `config.LoadRaw` provides the un-merged config so explicit-vs-default is real
(plain `Load` folds block-level defaults, destroying the signal — and misrenders partial blocks
like `retry:{max_attempts:6}` as `backoff_base: 0s`). Human output tags each field `(explicit)`/
`(default)`; `--json` emits `{value,source}`. Plain `config show` is unchanged. Covers the silent
majority (plugin retry/timeouts/breaker/parallelism); service/state/api defaults are already
materialized by `applyConfigDefaults`. Tests in `internal/config/effective_test.go` +
`cmd/ductile/config_effective_test.go`; docs in CONFIG_REFERENCE.md §4.2.2. Acceptance met:
`config show --effective birda` → `retry.max_attempts: 4 (default)` / `timeouts.handle: 5m0s (explicit)`.
Follow-up [[75-config-unify-plugin-default-resolvers]] tracks collapsing the duplicated runtime
resolvers onto one source so the view can't drift.

# `config show` should render the *effective* config (resolve plugin defaults), not just the source

**Origin (Hickey "hidden values" review, 2026-06-05, off [[68-deploy-vault-branch-unraid]]):** the #68
cleanup dropped inert flat keys so plugins fall back to defaults. But ductile has **no resolved-effective
view**: `applyConfigDefaults` (`internal/config/loader.go:613`) fills only service/state/api defaults and
**never iterates plugins**; per-plugin retry/timeout defaults resolve *lazily at dispatch*
(`MaxAttemptsForPlugin` → `DefaultPluginConf()`, `types.go:281`). So a plugin that omits `retry`/`timeouts`
runs on `max_attempts: 4` / `handle: 120s` — values that live **only in Go** and are invisible to the
config file *and* to `config show` (which renders the un-materialized struct). The governing value isn't
where an operator would look. Implicit state in the Hickey sense.

**Design stance (chosen over the alternatives):**
- NOT "write every default into every plugin stanza" — that copies one fact across ~40 sites and lets a
  changed global default go silently stale (duplicated-value: a lateral move, not a win).
- NOT "no code defaults at all" — wrong simplicity trade.
- YES: keep defaults **single-sourced** in code, make them **discoverable on demand**. Decomplect
  "where the value is defined" from "can I see what's in force."

## Do
- Add an effective/resolved rendering to `config show` (and `config get`): fold `DefaultPluginConf` into
  each plugin before output. Consider a `--effective` flag (default raw) or make resolved the default.
- Tag each value `explicit` vs `default` in the output so the operator sees which are file-set vs inherited.
- Keep intentional deviations explicit in source (e.g. the two 300s timeouts); this view covers the
  silent majority.

**Acceptance:** `config show --effective birda` shows `max_attempts: 4 (default)` and
`timeouts.handle: 300s (explicit)` — what runs is one query away, with no value duplicated into the source
files.
