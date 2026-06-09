---
id: 134
status: backlog
priority: Medium
tags: [runtime, reload, posture, vault, fail-closed]
---

# Reload-restore rebuilds with nil vaultOwner — armed management-posture box strands on a failed reload

> Raised 2026-06-10, dual luminary code review of `feat/129-vault-operable-posture`
> (Lamport×Thomas/Hunt F2).

## The concern
Inside `buildRuntime`, vault presence is computed two different ways: the `ValidateConfigOnBoot`
doctor gate reads `opts.vaultOwner != nil` (`cmd/ductile/runtime.go:457`), while the posture
decision uses the post-fallback owner (`LoadVault` reload at `runtime.go:612-619`, posture at
`:635`). The reload-failure **restore** branch (`runtime.go:193-195`) passes no owner — so on a box
with `validate_config_on_boot` armed and a zero-token (management-posture) running config, a failed
reload's restore sees `vaultPresent=false`, errors on `api.auth.tokens`, the restore fails, and
`rm.runtime = nil` (`:199`) with the old listeners already stopped (`:174-181`). All future SIGHUPs
answer "runtime not available" — exactly the stranded half-state #130 exists to prevent, reachable
by one typo'd reload during bootstrap.

## Fix
Move the `vaultOwner` fallback (`runtime.go:612-619`) **above** the admission doctor gate and feed
`WithVaultPresent(vaultOwner != nil)` from the resolved value — one computation for every consumer
in the function. Optionally thread the old runtime's owner into the restore call.

## Done when
A failed reload on an armed, management-posture box restores the previous runtime; covered by a
test (reload broken config under `validate_config_on_boot` + zero tokens + vault).
