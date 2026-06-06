---
id: 72
status: done
priority: Normal
blocked_by: [48]
tags: [vault, tokens-yaml, decomplect, cleanup, cli]
---

# Retire the tokens.yaml *file* surface (config token CLI + file types) — #48 slice 3b

**DONE 2026-06-06 — acceptance met.** Three-host cutover complete (MacM1/ThinkPad/Unraid all
tokens.yaml-free, vault sole source, verified incl. cross-node relay HMAC). Final code step landed:
removed `dedicatedScopeDomains["tokens"]` + the loader's tokens.yaml scope-file recognition
(`loader.go` DiscoverScopeDirs + verifyScopeFilesRecursively) + the tokens.yaml entries in the
backup/checksum lists (`config_manage.go`); fixed the now-false comments (age.go, types.go). Tests
re-pointed to webhooks.yaml (TestHashVerification, TestDiscoverScopeDirsFromIncludes); obsolete
TestStrictDecodeWarningsSkipsTokensScope removed. `grep -r tokens.yaml` over non-test code returns
only historical comments. `go build ./...` + `go test ./...` green (2 pre-existing dispatch timing
flakes pass isolated).

> Status note (2026-06-06): primary surface deletion **shipped** (`a7e592c`); card stays `doing` because
> its acceptance (`grep -r tokens.yaml` clean + per-instance include/file drop) is not yet met. The
> residual cleanup below (stale comments, dead-residue sweep, sample-filename test renames, shim removal)
> is consolidated into [[73-tokens-yaml-retirement-branch-review-punchlist]] to avoid duplicate tracking.

## DONE (2026-06-06) — user-facing surface deleted
- Deleted the `ductile config token` + `config scope` CLIs (both managed tokens.yaml) and their routing,
  help, and ~700 lines of handlers/helpers in `config_manage.go`.
- Deleted `config.TokenEntry`, `config.TokensFileConfig`, the `ConfigFiles.Tokens` field + its discovery
  and FileTier/AllFiles/HighSecurityFiles uses.
- Fixed the user-facing validator error: `secret_ref … not found in the vault` (dropped "or tokens.yaml").
- Snapshot secret-use source label `tokens.yaml` → `vault`.
- Tests reworked onto webhooks.yaml as the high-security fixture (integrity/strict-reload/hash tests);
  `go test ./...` green, `golangci-lint ./...` 0 issues.

## DELIBERATELY RETAINED — deploy-safety shim (remove in final step)
- `dedicatedScopeDomains["tokens"]` (strict_decode.go) + the loader's scope-file recognition of tokens.yaml:
  these keep a lingering tokens.yaml *include* a harmless no-op so the prod boxes (armed
  `validate_config_on_boot`) don't crash-loop. Remove ONLY after each instance drops the include + file.

## REMAINING (cosmetic, for full `grep -r tokens.yaml` cleanliness)
- Stale comments mentioning tokens.yaml (webhook docs, types.go, secrets/age.go, vault_secrets.go).
- A few tests use "tokens.yaml" as a *sample* filename for generic file handling (encrypted-include decrypt,
  config backup/restore) — rename to another file.
- Per-instance: drop the tokens.yaml include + file, then remove the shim above. Then acceptance is met.

**Origin ([[48-epic-retire-tokens-yaml]] slice 3a, 2026-06-06):** slice 3a removed tokens.yaml as a
*runtime* secret source (the vault is now sole source). But the tokens.yaml *file* machinery still exists
and is now **dead-but-isolated**: it manages a file the runtime ignores.

## What's left to delete
- `ductile config token` CLI (`cmd/ductile/config_manage.go`: create/list/add/remove/set/inspect/rehash on
  the tokens.yaml file) — `config token add` now writes an entry nothing reads.
- `config.TokensFileConfig`, `config.TokenEntry`, and `ConfigFiles.Tokens` (the tokens.yaml path field) +
  its discovery (`discovery.go`) and include/scope-file handling.
- `dedicatedScopeDomains["tokens"]` in `strict_decode.go` (the no-op skip for a tokens.yaml include) —
  remove once configs no longer carry the include.
- Associated tests: `config_tokens_guard_test`, the tokens.yaml file-handling tests in
  `hash/integrity/discovery/strict_decode`, etc.

## Sequencing / deploy
- Per-instance: drop the `tokens.yaml` include from each config (Thinkpad/Mac/Unraid) and remove the file,
  then redeploy. Until then, leave `dedicatedScopeDomains["tokens"]` so a lingering include stays a no-op.
- Consider a deprecation guard in the interim: `config token add/set` prints "tokens.yaml is no longer a
  secret source — use 'ductile vault set'" so nobody is misled before the CLI is removed.

**Acceptance:** `grep -r tokens.yaml` finds only history; a fresh deploy with no tokens.yaml resolves every
`secret_ref` from the vault (the #48 epic acceptance, fully met).
