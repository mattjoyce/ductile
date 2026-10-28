---
id: 151
status: backlog
priority: Low
tags: [doctor, auth, config-check, cleanup]
depends_on: [145]
---

# Remove doctor's pre-policy scope grammar

> After card 145, `config.validate` refuses every scope `auth.KnownScope` does not know, before
> `doctor` runs. `doctor.validateSingleScope` still parses the scope grammar that existed before the
> policy: plugin-named scopes (`echo:ro`, `gmail:allow:send`, `gmail:deny:x`) and low-level
> `read:`/`trigger:`/`admin:` scopes. None of those can reach it any more, so the branch is dead,
> and its tests exercise it by calling `doctor` directly on a config `validate` would refuse.

## Problem now (after card 145 merges)

- `internal/doctor/doctor.go` `validateTokenScopes` / `validateSingleScope`: for every scope the
  policy knows (`*`, `plugin:*`, `jobs:*`, `events:*`, `system:*`) it returns early with no issue.
  The plugin lookup, `allow:`/`deny:` command checks and the "unknown plugin" error only fire on
  scopes `validate` already refused.
- Latent bug: a plugin actually named `jobs`, `events`, `system` or `plugin` is found by
  `registry.Get` first, so `jobs:status:ro` would be judged as plugin `jobs` with access
  `status:ro` and reported as an invalid access type.
- Tests on the dead grammar: `internal/doctor/doctor_test.go` `TestValidate_TokenScopeValidPlugin`
  (`echo:ro`), `TestValidate_TokenScopeUnknownPlugin`, `TestValidate_TokenScopeInvalidCommand`,
  `TestValidate_TokenScopeLowLevel`.

## Change

1. Verify first: every `doctor.New(...)` caller (`cmd/ductile/config.go:690`,
   `config_manage.go:106`, `runtime.go:558`) gets its config from a load that ran `validate`. If
   one does not, stop and report; the branch is then not dead for that caller.
2. Delete `validateSingleScope` and the four tests above. `validateTokenScopes` keeps only the
   reserved-scope warning (`auth.ReservedScope`, added in card 145).
3. Add one doctor test: a token with `jobs:status:ro` and a registered plugin named `jobs` gives no
   error (pins the latent bug closed).
4. Docs: any CONFIG_REFERENCE or skill text that still documents plugin-named or `allow:`/`deny:`
   token scopes is corrected to the policy's scope list (`auth.KnownScopes()`).

## Done

- No plugin-named scope grammar left in `internal/doctor`; the new test passes; merge-grade gates.

## History

- 2026-10-28: carded from the card 145 policy-name review.
