---
id: 72
status: backlog
priority: Normal
blocked_by: [48]
tags: [vault, tokens-yaml, decomplect, cleanup, cli]
---

# Retire the tokens.yaml *file* surface (config token CLI + file types) — #48 slice 3b

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
