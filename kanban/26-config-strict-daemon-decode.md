---
id: 26
status: done
priority: Normal
blocked_by: [17]
tags: [config, schema, admission, hardening]
---

# Config · enforce the embedded schema at daemon load (strict unknown-key handling)

The embedded JSON schema is invoked only by the `config validate` / `config schema` CLI
(`internal/configschema/configschema.go`); the daemon's load-time decode is a plain `yaml.Unmarshal`
with **no `KnownFields(true)`** (`internal/config/loader.go:443`), so unknown/typo'd keys are silently
ignored at runtime. This "satisfies §11's letter, not its spirit" (Lamport-Thomas-Hunt §6/§7).

**Scope:**
- Either invoke the embedded schema at daemon load, or add `KnownFields(true)` to the daemon decode.
- Pick one source of truth for "valid config" and make it warn-then-fail per ADR §11 (respecting the
  `validate_config_on_boot` toggle from #17).

**Acceptance:** an unknown top-level/section key in a loaded config surfaces (warn or fail per policy) at
daemon start, not silently ignored; CLI validate and daemon load agree on what's valid.

## Narrative
- **Source:** Lamport-Thomas-Hunt Branch-Review §6 + §7 (row 10).
- **Not covered by:** #17 decomplected `strict_mode` → `validate_config_on_boot` but did not make the boot
  decode strict about unknown keys.

### Done — WARN-ONLY (2026-06-03, commit `c78e553`)
**Pivot:** the original plan (invoke the embedded schema at boot, hard-fail per `validate_config_on_boot`)
proved unshippable — verified that BOTH the embedded schemas AND the example configs are drifted from the
`Config` struct, so a hard gate would reject ductile's OWN shipped configs. Full evidence captured in
[[36-config-schema-struct-drift]]. Operator chose: ship the visibility now (warn-only), defer the hard gate
to after the drift cleanup.
- **Mechanism — struct, not schema:** `config.StrictDecodeWarnings(cfg)` re-decodes each loaded source file
  strictly (`yaml KnownFields(true)`) against the `Config` struct — the thing that actually runs — and
  returns a warning per key the lenient load dropped. The drifted embedded schema is NOT used (it would
  false-positive on valid configs).
- **Warn-only:** `buildRuntime` logs each warning at boot; the load itself stays lenient, so a config the
  daemon already accepts keeps booting. Catches nested typos (e.g. a mis-keyed `service.*` admission field),
  not just top-level — proven by test.
- **No false positives:** files carrying only a dedicated-scope domain (`pipelines`, `yaml:"-"` in Config,
  loaded by a separate path) are skipped; `${ENV}` type-mismatch lines are filtered so only unknown-field
  warnings surface. Uses the loader's own `interpolateEnv` so the strict pass sees what the lenient pass saw.
- Gate: gofmt/vet clean, golangci-lint 0 issues, `-race` green on config + cmd.
- **Deferred to [[36-config-schema-struct-drift]]:** reconcile schemas + example configs with the struct,
  then lift this from warn-only to warn-then-fail per `validate_config_on_boot` (the original intent).

### Done — HARD GATE (2026-06-04)
The deferred flip, now that [[36-config-schema-struct-drift]] reconciled schemas+configs and a strict
KnownFields decode is verified clean on every shipped config.
- **Mechanism:** new `config.StrictDecodeError(cfg)` composes the `StrictDecodeWarnings` dropped-key set
  into one error (nil when clean) — the struct decode, NOT the embedded JSON schema (that stays the CLI lint).
- **Gate:** `buildRuntime` still logs every dropped key as a warning (visibility preserved), then, when
  `service.admission.validate_config_on_boot` is set, calls `StrictDecodeError` and **fails** the build —
  the error names the dropped key(s) AND the toggle to disable. Warn-only when the toggle is off.
- **Boot + reload parity:** `buildRuntime` is reused on reload (`runtime.go:177`), so the gate covers both,
  matching the existing `doctor.Validate()` check under the same toggle. No special-casing.
- **Verified:** unit (`TestStrictDecodeError{Clean,NamesDroppedKey}`); gofmt/vet/golangci-lint(0)/`-race`
  green on internal/config + cmd/ductile; live `system start` e2e — FAIL: typo'd key + toggle → warning
  logged then `Failed to start runtime: configuration validation failed: 1 ignored config key(s)…`, exit 1;
  PASS: clean config + toggle → boots past the gate (`ductile starting` → `database opened` → scheduler).
  Toggle-off stays warn-only (gate is inside the `if admission.ValidateConfigOnBoot` branch).
