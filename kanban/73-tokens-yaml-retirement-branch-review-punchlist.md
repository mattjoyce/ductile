---
id: 73
status: backlog
priority: Low
tags: [vault, config, tokens-yaml, branch-review, decomplect, cleanup, backlog]
---

# #48 tokens.yaml-retirement · branch-review punch-list (2026-06-06)

Consolidated follow-ups from the four 2026-06-06 branch reviews of the commits above `3b33b9a`
(`fbad7a0` → `8917cf8` → `0baf11e` → `a7e592c`, epic [[48-epic-retire-tokens-yaml]] / [[72-retire-tokens-yaml-file-surface]]).

All four reviewers agree the slice itself is the strongest in the branch — a genuine *decomplecting deletion*
(net −1,400 / −1,971 lines; two secret sources collapsed to one; `cfg.Tokens []TokenEntry` →
`cfg.ResolvedSecrets map[string]string`; ADR §8.5 "if it's a secret, it's in the vault" now actually enforced;
fail-closed preserved end-to-end; no value-leak introduced by the rename — verified by Hickey + Lamport on the
snapshot/API view surfaces). **Nothing here blocks merge.** The debt is the *trailing edge* of the cut: the
architecture landed clean, the sweep behind it didn't quite finish.

Grouped to keep the board readable — split into its own card when picked up (follows
[[27-vault-hardening-punchlist]] / [[47-vault-hardening-punchlist-ii]]).

## Items

- [~] **(MED) Reload + boot-verify paths re-pay the #43 decrypt — single-decrypt is start-only.**
  The one substantive carry-forward; raised independently by Ousterhout (6c, verified) and Hickey (§1.1, "partial").
  **Reload half FIXED 2026-06-06; boot-verify half still open.**
  - [x] *Reload (Ousterhout 6c) — FIXED.* `reloadManager.Reload` (`runtime.go:154`) called the owner-less
    `config.Load`, which decrypted the blob via `projectVaultSecrets` then discarded the owner; `buildRuntime`
    then found `opts.vaultOwner == nil` (`runtime.go:599`) and called `config.LoadVault` → a **second decrypt**.
    Now `config.LoadWithVault` returns the owner and it is threaded into `buildRuntime` via
    `runtimeBuildOptions.vaultOwner`, collapsing the two reload-path decrypts into one — same fix the start path
    already uses (#43). Behaviorally identical: `LoadWithVault`'s owner is the same `loadVaultOwner`/`vault.Load`
    object `LoadVault` produces. `go build` + `go test ./cmd/ductile ./internal/config` + `golangci-lint` clean.
  - [ ] *Boot integrity-verify (Hickey §1.1) — STILL OPEN.* With `verify_integrity_on_boot` on,
    `verifyReloadIntegrity` → `verifyPluginFingerprintsForConfig` re-`config.Load`s (`config_manage.go:234`) and
    `fingerprintNonceForConfig` does its own `config.LoadVault` for the nonce (`config_manage.go:296`) → 2 extra
    decrypts at boot *and* on every reload, and the nonce-vs-owner reads stay separate (the TOCTOU Hickey flagged
    persists). Threading the owner here is a broader change: those funcs take only `configPath` and are shared
    with the `plugin lock` CLI (`plugin_lock.go:94,142`) and ~12 test callers — needs an owner-accepting internal
    variant behind the existing public signatures. Deferred as its own change. Remaining part of
    [[43-vault-single-load-thread-nonce-boot]]; efficiency + minor TOCTOU cleanup, **not** a security hole.

- [ ] **(MED) Dangling `vault import` verb + stale CLI help print "Unknown action".**
  Ousterhout's sharpest delta finding (6a, user-facing); also Lamport T2 / Hickey.
  `printVaultNounHelp` (`cmd/ductile/vault.go:77`) still advertises `import   Migrate tokens.yaml entries…
  [--config --tokens --resolve-env]`, but `runVaultNoun` has no `case "import"` and `runVaultImport` +
  `internal/config/vault_import.go` were deleted → `ductile vault import` falls through to
  `"Unknown vault action: import"` (which prints the help that advertises it). Same for the deleted
  `config token` / `config scope` help (`cmd/ductile/main.go:255-256`).
  *Decision needed:* either **restore the `import` verb** (it is the on-ramp an operator with an old
  `tokens.yaml` still needs) **or delete the help lines**. Help and dispatch must agree.

- [ ] **(LOW) Sweep stale comments/doc-drift that still narrate the retired `tokens.yaml`/graft world.**
  Flagged by all four; navigation hazard in an AI-built codebase where doc-comments are the stated contract.
  Survivors: `loader.go:124` ("graft … legacy resolution table / coexistence window"), `loader.go:133`
  ("Hash-verify scope files (tokens.yaml, webhooks.yaml)"), `vault_secrets.go:24/60/101/102/124/175`
  (`activeVaultSecrets` "graft into cfg.Tokens", `pluginScopedSecret` "migrated tokens.yaml value",
  `vaultBlind` "No vault → tokens.yaml is authoritative"), `loadVaultOwner` (refers to renamed-away
  `graftVaultSecrets`), `logGraftWarnings` (named "graft", "coexistence-window collision warnings" — there is
  no merge/collision now, these are blast-radius warnings), `internal/webhook/types.go:39`
  ("SecretRef references a secret in tokens.yaml"). Behaviour is correct; the words describe a dead world.

- [ ] **(LOW) Bump `SnapshotFormat` (or restore dropped token fields).**
  Lamport T1 — the bug-hunt's "HIGH" was **disproven**: `sanitizeConfig` now emits tokens as `{name, key}` and
  labels the source `vault` (`configsnapshot/snapshot.go:288-302`), changing the appended `config_hash` shape on
  upgrade, but this is **cosmetic** — `FailOnDrift` drives `.checksums` integrity drift via `verifyReloadIntegrity`
  (`runtime.go:161,436`), not a boot-to-boot `config_hash` compare; snapshots are append-only identity records.
  Real residual: `SnapshotFormat` stayed `1` despite the serialization shape changing — bump it so the format
  version tracks the shape.

- [ ] **(LOW) Track deletion of the `tokens.yaml` no-op shim once prod boxes drop the include.**
  Brooks / Lamport T3 — a *named temporary*: `dedicatedScopeDomains["tokens"]` (`config/strict_decode.go:24-27`)
  + loader recognition (`loader.go:339-343`) keep a lingering `tokens.yaml` include a **no-op** so armed
  `validate_config_on_boot` boxes don't crash-loop mid-migration (an unmigrated `tokens.yaml` is still fail-closed —
  its `secret_ref`s error at `Load`). Marked for removal "once each instance drops the include + file"; the only
  risk is it outliving its purpose. Delete after the M1 / Unraid / ThinkPad boxes ([[67-deploy-vault-branch-macm1]],
  [[68-deploy-vault-branch-unraid]], [[49-epic-thinkpad-vault-field-trial]]) drop the include.

- [ ] **(LOW) Dead-residue sweep.** Ousterhout 6d — `TokensConfig` (`types.go:356`, the flat `tokens.yaml` model)
  has no remaining non-test referent; the `tokens.yaml` half of `verifyScopeFilesRecursively` is now unreachable
  for authoring (webhooks.yaml remains). Delete, or annotate as intentional back-compat.

- [ ] **(MED · verify) Confirm inbound API bearer-token authz kept a home after `tokens.yaml` retirement.**
  Brooks's one honest residual — he verified the inbound *scope domain* (`cf.Scopes`, `scopes/*.json`, `auth.go`)
  survives the `TokenEntry.ScopesFile` deletion, but did **not** fully trace where inbound API *bearer-token
  definitions* now live post-`tokens.yaml`. Confirm API-token authz was not orphaned by the cutover.

## Notes (no action — captured for the trail)

- **Implicit load-order contract (Ousterhout substitutability note).** `cfg.ResolvedSecrets` is an in-memory
  projection (`yaml:"-"`) populated as a *side effect* of `config.Load` → `projectVaultSecrets`. A future loader
  that skipped the projection would leave webhook/relay secrets unresolved with **no compile-time signal**. The
  population is an implicit load-order contract, not an enforced one — worth hardening if/when the `dvault` daemon
  swap (form C) introduces an alternate load path.
- **Two secret-propagation classes (Lamport T4, by design).** A `vault roll` of a gateway-consumer secret
  (webhook/relay HMAC, served from the load-time `cfg.ResolvedSecrets` snapshot) takes effect only after a config
  **reload**; a plugin secret (delivered via `Compose` at spawn) re-resolves at the **next spawn**. Documented in
  OPERATOR_GUIDE — just two transitions an operator must hold.
- **Pre-existing `internal/dispatch` timing flakes (Brooks, out of #48 scope).** Whole-suite FAILs on
  `TestDispatcher_Start_ParallelExecution` (wall-clock assertion) + `TestSpawnPluginTimeoutKillsProcessGroup`
  **pass on isolated re-run** and sit in code #48 never touches — test-quality debt, not a #48 regression. Worth
  its own card if it recurs.
