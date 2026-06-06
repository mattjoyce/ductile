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

- [x] **(MED) Reload + boot-verify paths re-pay the #43 decrypt — single-decrypt is start-only.**
  The one substantive carry-forward; raised independently by Ousterhout (6c, verified) and Hickey (§1.1, "partial").
  **Both halves FIXED 2026-06-06.**
  - [x] *Reload (Ousterhout 6c) — FIXED.* `reloadManager.Reload` (`runtime.go:154`) called the owner-less
    `config.Load`, which decrypted the blob via `projectVaultSecrets` then discarded the owner; `buildRuntime`
    then found `opts.vaultOwner == nil` (`runtime.go:599`) and called `config.LoadVault` → a **second decrypt**.
    Now `config.LoadWithVault` returns the owner and it is threaded into `buildRuntime` via
    `runtimeBuildOptions.vaultOwner`, collapsing the two reload-path decrypts into one — same fix the start path
    already uses (#43). Behaviorally identical: `LoadWithVault`'s owner is the same `loadVaultOwner`/`vault.Load`
    object `LoadVault` produces. `go build` + `go test ./cmd/ductile ./internal/config` + `golangci-lint` clean.
  - [x] *Boot integrity-verify (Hickey §1.1) — FIXED 2026-06-06.* Threaded an optional `owner *vault.Vault`
    through `verifyReloadIntegrity` → `verifyPluginFingerprintsForConfig` → `fingerprintNonceForConfig`. Boot
    passes `opts.vaultOwner`, reload passes `newOwner` (both the one `LoadWithVault` decrypt); the `plugin lock`
    CLI (`plugin_lock.go:94,142`) and ~12 test callers pass `nil` → fall back to `config.LoadVault` (unchanged,
    still fail-closed). The vault re-decrypt for the nonce is gone on boot and reload, and the nonce now comes
    from the same snapshot that delivers secrets → TOCTOU window closed. Done as part of
    [[43-vault-single-load-thread-nonce-boot]]; new test `TestFingerprintNonceForConfigReusesOwnerWithoutDiskLoad`.
    (The `config.Load(configPath)` re-read at `config_manage.go:234` is config bytes, not vault — separate concern,
    left as-is.) `go build ./...` + `go vet ./cmd/ductile` + `go test ./cmd/ductile` clean.

- [x] **(MED) Dangling `vault import` verb + stale CLI help print "Unknown action".** FIXED `c5fcab5`:
  deleted the `import` help line from `printVaultNounHelp` (verb dispatch+impl were already gone; the
  on-ramp was a deliberate throwaway — operators with an old `tokens.yaml` use `vault set`). Also fixed
  the stale `secrets` help example (tokens.yaml→webhooks.yaml) and stopped `config init` scaffolding a
  `tokens.yaml` stub.
  Ousterhout's sharpest delta finding (6a, user-facing); also Lamport T2 / Hickey.
  `printVaultNounHelp` (`cmd/ductile/vault.go:77`) still advertises `import   Migrate tokens.yaml entries…
  [--config --tokens --resolve-env]`, but `runVaultNoun` has no `case "import"` and `runVaultImport` +
  `internal/config/vault_import.go` were deleted → `ductile vault import` falls through to
  `"Unknown vault action: import"` (which prints the help that advertises it). Same for the deleted
  `config token` / `config scope` help (`cmd/ductile/main.go:255-256`).
  *Decision needed:* either **restore the `import` verb** (it is the on-ramp an operator with an old
  `tokens.yaml` still needs) **or delete the help lines**. Help and dispatch must agree.

- [x] **(LOW) Sweep stale comments/doc-drift that still narrate the retired `tokens.yaml`/graft world.**
  FIXED `877e9bc`: renamed the "graft" vocabulary to "projection", dropped the dead `cfg.Tokens` /
  "tokens.yaml is authoritative" references, and renamed `logGraftWarnings`→`logSecretProjectionWarnings`
  (vault_secrets.go, loader.go, runtime.go, webhook types/doc). Left the still-accurate shim comments
  (loader scope-file handling) untouched — that machinery is live until the shim is removed (see below).
  Flagged by all four; navigation hazard in an AI-built codebase where doc-comments are the stated contract.
  Survivors: `loader.go:124` ("graft … legacy resolution table / coexistence window"), `loader.go:133`
  ("Hash-verify scope files (tokens.yaml, webhooks.yaml)"), `vault_secrets.go:24/60/101/102/124/175`
  (`activeVaultSecrets` "graft into cfg.Tokens", `pluginScopedSecret` "migrated tokens.yaml value",
  `vaultBlind` "No vault → tokens.yaml is authoritative"), `loadVaultOwner` (refers to renamed-away
  `graftVaultSecrets`), `logGraftWarnings` (named "graft", "coexistence-window collision warnings" — there is
  no merge/collision now, these are blast-radius warnings), `internal/webhook/types.go:39`
  ("SecretRef references a secret in tokens.yaml"). Behaviour is correct; the words describe a dead world.

- [x] **(LOW) Bump `SnapshotFormat` (or restore dropped token fields).** FIXED `877e9bc`: `SnapshotFormat`
  `1`→`2` with a comment recording the {name,key} vault-sourced shape change.
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

- [x] **(LOW) Dead-residue sweep.** FIXED `877e9bc`: deleted the dead `config.TokensConfig` type (zero
  referents, even in tests). The `tokens.yaml` half of `verifyScopeFilesRecursively` is **intentionally
  retained** — it is the live deploy-safety shim (see next item), not dead code.

- [x] **(MED · verify) Confirm inbound API bearer-token authz kept a home after `tokens.yaml` retirement.**
  CONFIRMED 2026-06-06: inbound API bearer tokens live in `cfg.API.Auth.Tokens` (config-native, never
  `tokens.yaml`) and are checked by `auth.Authenticate(token, s.config.Tokens)` (`internal/api/auth.go:23`);
  validated by `loader.go` + `doctor.go`, snapshotted by fingerprint only. Wholly independent of the retired
  `tokens.yaml`/`TokenEntry` path — not orphaned by the cutover.

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
