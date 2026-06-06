---
id: 17
status: done
priority: Normal
blocked_by: []
tags: [config, admission, runtime, decomplect]
---

# Cross-cutting · decomplect `strict_mode` into a named admission block

`service.strict_mode` welded four unrelated admission-control policies into one
boolean. Split them into independent, self-describing gates; keep `strict_mode`
as a deprecated alias (coexistence window, like `tokens.yaml`).

**Problem:** `strict_mode: true` simultaneously (P1) ran the integrity preflight
at boot, (P2) promoted operational drift to a hard fail, (P3) required config
validation at boot, and (P4) required API auth — with no way to choose a subset.
And the name never said *strict about what*.

**Scope:**
- New `service.admission` block: `verify_integrity_on_boot`, `fail_on_drift`,
  `validate_config_on_boot`, `require_api_auth`. Each independent, default false.
- `strict_mode: true` → deprecated alias enabling all four; logs a warning;
  an explicit `admission` block wins and supersedes it.
- `config check --fail-on-warnings` primary; `--strict` deprecated alias.

**Acceptance:** an admission block can enable any one gate without the others;
`strict_mode: true` still behaves as before; reload sources `fail_on_drift` from
the running config (no relax-via-reload); snapshot reflects the resolved policy.

## Narrative
- **Source:** operator directive (this session) — three corrections landed here:
  spawn-env bridge reverted to explicit `plugin_env_passthrough`, then "rename/
  decomplect strict" chosen as full 4-way split + alias, names approved inline.
- **Lens (Hickey / complecting):** one boolean braided four orthogonal policies.
  The fix is data: an `AdmissionConfig` of four named bools, resolved by one pure
  method (`AdmissionPolicy`) that applies the alias. Runtime reads the resolved
  policy, not the raw flag.
- **Security invariant kept:** reload still sources `fail_on_drift` from the
  *running* config, so an attacker can't disable drift enforcement in the reload
  they are pushing.

### Done 2026-06-01
- `internal/config/types.go`: `AdmissionConfig{VerifyIntegrityOnBoot, FailOnDrift,
  ValidateConfigOnBoot, RequireAPIAuth}`; `Service.Admission *AdmissionConfig`;
  `StrictMode` kept + marked deprecated. Pure resolvers `AdmissionPolicy()` and
  `StrictModeDeprecationWarning()`.
- `cmd/ductile/runtime.go`: startup gate split into three independent `if`
  blocks; deprecation warning logged; `verifyReloadIntegrity` param renamed
  `strict`→`failOnDrift`, message now `(admission.fail_on_drift)`; reload sources
  the policy from the running config.
- `internal/configsnapshot/snapshot.go`: emits a single resolved `admission`
  view (`renderAdmission`) — the raw `strict_mode` key was dropped so there is
  one authoritative view (the sanitized map is not hashed; `sourceHash` covers
  integrity).
- `cmd/ductile/config.go`: `--fail-on-warnings` primary, `--strict` deprecated alias.
- Docs: `CONFIG_REFERENCE.md` (example + tier table) and `OPERATOR_GUIDE.md`
  rewritten to the admission block with a deprecation note.
- Tests: `internal/config/admission_test.go` (explicit-wins, alias-enables-all,
  permissive default, deprecation-warning, YAML round-trip); `runtime_strict_test.go`
  message assertion updated. `go build`, `go vet`, full `go test ./...` green
  (0 failures).
- **Not done (out of scope):** per-doc cleanup of incidental `strict_mode`
  mentions in `MACOS_INSTALLATION.md`/`WEBHOOKS.md` — still correct via the alias.
