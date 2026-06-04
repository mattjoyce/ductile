---
id: 36
status: done
priority: Normal
blocked_by: []
tags: [config, schema, drift, hardening, hygiene]
---

# Config · reconcile embedded schemas + example configs with the real struct (drift)

**Surfaced 2026-06-03 while doing #26 (strict boot decode).** Turning on *any* boot-time
strictness today would reject ductile's own shipped configs, because both the embedded JSON
schemas AND the example configs have drifted from the actual `config.Config` struct. #26 shipped
**warn-only** for exactly this reason; the hard-fail gate is blocked on this cleanup.

## Verified findings

**Example configs carry silently-dropped keys** (proven by a strict `KnownFields(true)` decode):
- `config/config.yaml` (include-mode root): `log_level` is at the **root**, not under `service:`
  (`field log_level not found in type config.Config`). It is silently ignored — the operator's
  intended log level is not applied.
- `config/plugins.yaml`: every plugin entry uses `timeout` and `max_attempts` directly
  (`field timeout / max_attempts not found in type config.PluginConf`, 12×). The struct expects
  `timeouts:`/`retry.max_attempts`. These are silently ignored — dead config that looks live.

**Embedded schemas reject working configs** (proven via `ductile config validate`):
- `schemas/config.schema.json` requires `service`/`state`/`plugin_roots`, so any single-section
  include file (e.g. `api.yaml`) fails against the `config` schema.
- `schemas/webhooks.schema.json` wants `webhooks` as an **array**, but real `config/webhooks.yaml`
  is an **object** (`got object, want array`).
- `schemas/plugins.schema.json` rejects the real `timeout`/`max_attempts` keys above.
- `schemas/config.schema.json` `service` block is **unconstrained** (`additionalProperties` unset,
  no `properties` listed), so it catches *no* typo nested under `service` — including the very
  admission keys from #17 (`validate_config_on_boot`).
- `config.yaml` (single-file) fails the schema on `plugins.echo.config: null` (empty `config:`).

So `ductile config validate` currently disagrees with configs the daemon happily runs — the
schema is not actually authoritative.

## Scope
- Decide the single source of truth for "valid config" — the Go struct vs. the JSON schemas — and
  reconcile the other to it (the struct is what actually runs).
- Fix the example configs: move `log_level` under `service`; correct `plugins.yaml` to the
  supported `timeouts:`/`retry:` shape (or add `timeout`/`max_attempts` to `PluginConf` if they are
  meant to be supported shorthand — an explicit decision).
- Tighten the schemas to match reality: `service.additionalProperties:false` with the real
  properties (so nested typos are caught); `webhooks` object-vs-array; `plugins` retry/timeout
  shape; single-section include files validated against the right per-file schema, not `config`.
- Once `config validate` passes on every shipped config AND a strict struct-decode is clean, lift
  #26 from warn-only to **warn-then-fail per `validate_config_on_boot`** (the original #26 intent).

**Acceptance:** `ductile config validate` passes on every config under `config/` and `config.yaml`;
a strict `KnownFields` decode of those files is clean; #26 can then enable the hard-fail gate
without regressing a valid config.

## Narrative
- **Source:** #26 investigation (2026-06-03) — the schema and struct were both found drifted from
  the example configs.
- **Relates to:** [[26-config-strict-daemon-decode]] (warn-only now; this unblocks the hard gate),
  [[17-decomplect-strict-admission]] (the admission keys a tightened `service` schema would protect).

## Decisions (operator, 2026-06-04)
- **D1 — timeout/max_attempts:** the Go struct is the single source of truth (it's what runs).
  The flat `timeout:`/`max_attempts:` keys are NOT `PluginConf` fields; they were silently dropped.
  Fixed the EXAMPLE configs to the supported nested shape (`timeouts:{poll,handle}` + `retry:{max_attempts}`)
  rather than adding flat shorthand to the struct. A single operator duration maps to both core
  lifecycle commands (poll+handle), since the flat key had no faithful 1:1 target.
- **D2 — schema architecture:** consolidate to schemas keyed by *artifact identity*, one definition
  per type. THREE schemas: `config` (all config; lenient root + `#/$defs/WholeConfig` strict overlay),
  `pipelines` (self-contained DSL, kept), `plugin-manifest` (separate trust domain, kept). DELETED the
  drifted duplicates `include`/`plugins`/`webhooks`/`routes`/`relay-ingress`/`relay-instances` — they
  were hand-copies of config's `$defs` and were the source of the array-vs-object / timeout drift.
- **D3 — validate UX:** `config validate --file X` defaults to the lenient root (validates whole files
  AND include fragments; no false missing-service errors). `--whole` asserts a complete config. The
  deleted `--name` values alias to `config` with a one-line notice (configschema.FoldedInto).
- **D4 — #26 flip OUT of scope:** this card lands consolidation + config fixes + clean validation.
  Lifting #26 from warn-only to a hard `validate_config_on_boot` gate (against `#/$defs/WholeConfig`)
  is left as a separate #26 commit (+ Dell e2e). The `WholeConfig` overlay + `--whole` ship here as the
  ready hook. Note: JSON-schema validation is operator-CLI-only — the boot gate is the struct/doctor path.

## Done (2026-06-04)
- **Answered the embedding question:** schemas are embedded (`embed.go` `//go:embed schemas/*.json`)
  and `internal/configschema` reads from the embedded bytes, never disk — tamper-proof per ADR §11.
- **`schemas/config.schema.json`** rewritten as the single lenient config schema: dropped top-level +
  nested `required` (so split fragments like webhooks.listen / webhooks.endpoints each validate);
  kept `additionalProperties:false` + types/enums/patterns everywhere (the typo/unknown-key value).
  Added the drifted-out struct fields the old strict schema falsely rejected — `service.admission`
  (+ the four `*_retention` durations), `api.allowed_origins`, top-level `tcc_paths`/`pipelines`/`tokens`;
  made `plugins.*.config` accept null; added `#/$defs/WholeConfig` strict overlay.
- **Deleted** the six drifted duplicate schemas; updated `configschema_test.go` (cross-file ref test,
  fragment/whole/shorthand/admission/null regression tests, folded-name-disjoint guard).
- **`internal/configschema`**: `ValidateYAMLWhole` + `compileTarget(name, anchor)` + `FoldedInto`.
- **`cmd/ductile/config_schema.go`**: `--whole` flag, folded-name alias notice, updated help.
- **Example configs**: `config/config.yaml` (log_level → service, + name/tick_interval),
  `config/plugins.yaml` and root `config.test.yaml` (flat timeout/max_attempts → nested timeouts/retry).
- **Verified:** `config validate` passes on every config under `config/`, root `config.yaml`,
  `config.test.yaml`, `test/stress/config.stress.yaml` (lenient) and `--whole` on the complete ones;
  a strict `KnownFields` decode is clean on all of them; gofmt/vet/golangci-lint/gosec clean and
  `go test -race -shuffle=on` green on internal/config, internal/configschema, internal/api, cmd/ductile.
- **Scope excluded:** the 17 `test/fixtures/docker/*` configs share the flat-timeout pattern but are e2e
  fixtures — changing their timeouts alters test behavior; left for a follow-up. ADR §11 (Obsidian) is
  operator-owned; the decision is recorded here.
