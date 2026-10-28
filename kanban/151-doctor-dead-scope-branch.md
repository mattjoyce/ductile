---
id: 151
status: done
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

## Build spec (2026-10-28, for opencode)

Card 145 is merged (`674d7d1`). Do the four steps in "Change" above, exactly, with these details:

- Step 1 evidence: in your summary, name for each of the three `doctor.New` callers the load
  function it uses and the line in `internal/config/loader.go` where that path calls `validate`.
- Step 2: `validateTokenScopes` becomes, in full:
  ```go
  // validateTokenScopes warns on a reserved scope. Every other scope question is
  // settled before doctor runs: config.validate refuses a scope the token policy
  // does not know.
  func (d *Doctor) validateTokenScopes(r *Result) {
  	for i, token := range d.cfg.API.Auth.Tokens {
  		for j, scope := range token.Scopes {
  			if parent, reserved := auth.ReservedScope(scope); reserved {
  				field := fmt.Sprintf("api.auth.tokens[%d].scopes[%d]", i, j)
  				d.addWarning(r, "token_scopes", field,
  					fmt.Sprintf("scope %q is reserved and grants nothing yet; use %s", scope, parent))
  			}
  		}
  	}
  }
  ```
  Delete `validateSingleScope`. Remove any import that becomes unused.
- Step 2 tests to delete: `TestValidate_TokenScopeValidPlugin`, `TestValidate_TokenScopeUnknownPlugin`,
  `TestValidate_TokenScopeInvalidCommand`, `TestValidate_TokenScopeLowLevel`. Keep
  `TestValidate_TokenScopeReservedWarns`. If deleting a test leaves a helper unused, delete it.
- Step 3 test name: `TestValidate_TokenScopePluginNamedLikeResource`: register a plugin named `jobs`
  (build it the way `echoPlugin()` is built, with name `jobs`), token scopes `["jobs:status:ro"]`,
  assert `r.Valid` and no `token_scopes` error.
- Step 4: `git grep -n -E 'allow:|deny:|:allow|:deny' -- docs skills cmd/ductile/templates` and fix
  only text that documents token scopes; leave anything about other features alone. List each hit
  and what you did with it.
- CHANGELOG: none (the release entry is written separately).

Gate: the default Go gate. Out of scope: everything outside `internal/doctor` and the docs hits.
Do not tag, do not merge.

## Review 1 (2026-10-28, approved)

Round 1 commit `9d5e43b` (`or/deepseek/deepseek-v4.1-flash`, session `ses_e7df6b85bffeVyMDO2T7cDfwx2`),
gate passed (harness). Step 1 evidence: all three `doctor.New` callers load via
`config.LoadWithVault`, which runs `validate` at `loader.go:188`. `validateTokenScopes` is the spec
text; `validateSingleScope` and the four tests are gone; the plugin-named-`jobs` test passes.
Docs grep: four hits, all relay allowlists, none token scopes. Reviewer reran doctor and cmd tests
under `-race`, goimports and golangci-lint on `internal/doctor`: clean.
Noted, not fixed: `docs/ARCHITECTURE.md` section 9.4 still shows pre-policy scopes in a
`scopes_file` example.
- 2026-10-28: done, ships in v1.1.0.
