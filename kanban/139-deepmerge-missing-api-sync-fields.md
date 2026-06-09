---
id: 139
status: backlog
priority: Low
tags: [config, loader, include-merge, tests]
---

# deepMergeConfig still drops api.max_concurrent_sync / max_sync_timeout; the guard test under-asserts its own claim

> Raised 2026-06-10, dual luminary code review of `feat/129-vault-operable-posture`
> (Lamport×Thomas/Hunt F4).

## The concern
The branch's merge fix (cb1f114) added `ManagementSocket` and `AllowedOrigins` to the API merge
block (`internal/config/loader.go:513-527`), but `APIConfig` has six fields — `MaxConcurrentSync`
and `MaxSyncTimeout` are still never merged (only defaulted, `loader.go:688-693`), so setting them
in an **included** `api.yaml` (the layout every fixture and deploy uses) is silently ignored. The
new `TestDeepMergeConfigAPIFields` comment claims "an api: block from an INCLUDED file must carry
**every** field through", but it asserts only the four merged fields — the test passes while its
stated property is false.

## Fix
Add the two missing merge lines (`if src.API.MaxConcurrentSync != 0 { … }`, same for
`MaxSyncTimeout`); extend `TestDeepMergeConfigAPIFields` to set and assert all six fields so the
next added field can't silently miss the merge.

## Done when
The guard test sets every `APIConfig` field in an include and asserts each survives the merge.
