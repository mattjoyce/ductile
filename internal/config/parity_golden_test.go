package config

// parityGolden is the reviewed list of every difference between load() and
// loadAshlar() over the parity corpus (card 153 slice 3), each with both
// loaders' values pinned. Codes:
//
//   - BUG-FIXED: a "Problem now" bug in load() that the Ashlar path does not
//     have. Each has a check in parityBugChecks that asserts the exact value
//     and fails against load().
//   - TIGHTENED: Ashlar refuses something load() accepts, and the refusal is
//     decided: decision G, or Matt's 2026-11-04 ruling.
//   - GATED: the decision note decided to keep today's behaviour, and the
//     Ashlar path cannot yet. The entry names the decision and what it waits
//     on. TestAshlarCutoverReadiness fails while any GATED entry exists.
//   - UNDECIDED: no ruling. The harness fails on it.

type parityCode string

const (
	codeBugFixed  parityCode = "BUG-FIXED"
	codeTightened parityCode = "TIGHTENED"
	codeGated     parityCode = "GATED"
	codeUndecided parityCode = "UNDECIDED"
)

// parityClass is why a difference is accepted: the code, the decision that
// rules it, what a GATED entry waits on, and a note for the reviewer.
type parityClass struct {
	code     parityCode
	decision string
	waitsOn  string
	note     string
}

type parityEntry struct {
	legacy, ashlar string
	class          parityClass
}

type goldenRow struct {
	caseID, mode, key, legacy, ashlar string
	class                             parityClass
}

var parityGolden = goldenFromRows(parityGoldenRows)

func goldenFromRows(rows []goldenRow) map[string]parityEntry {
	out := make(map[string]parityEntry, len(rows))
	for _, r := range rows {
		out[r.caseID+" "+r.mode+" "+r.key] = parityEntry{legacy: r.legacy, ashlar: r.ashlar, class: r.class}
	}
	return out
}

// The reasons, one per decision.
var (
	bugDroppedKey = parityClass{
		code:     codeBugFixed,
		decision: "Problem now: the include merge drops whole keys",
		note:     "deepMergeConfig never copies this key from an include; load() keeps the root's value or the zero value. For accounts and unconfined the privsep boot gate then decides posture from a config the operator did not write",
	}
	bugNonZeroWins = parityClass{
		code:     codeBugFixed,
		decision: "Problem now: non-zero wins; decision I: present false, 0 and \"\" replace",
		note:     "an include can turn a boolean on but never off",
	}
	bugTextInterpolation = parityClass{
		code:     codeBugFixed,
		decision: "Problem now: interpolation is text substitution",
		note:     "a value containing ': ' changes document structure in load(); Ashlar interpolates the scalar",
	}
	tightUnresolved = parityClass{
		code:     codeTightened,
		decision: "G: unresolved ${VAR} fails closed (accepted outright)",
		note:     "Ashlar refuses at the reference with file:line:column; load() keeps the placeholder and, in full mode, may refuse later in validation. The literal's own test sets the variable; the harness runs it unset",
	}
	tightEmptyDoc = parityClass{
		code:     codeTightened,
		decision: "G: empty documents refused, after the live audit and a documented migration",
		note:     "load() reads an empty include as nothing",
	}
	tightAlias = parityClass{
		code:     codeTightened,
		decision: "G: YAML aliases refused, after the live audit and a documented migration",
		note:     "load() resolves the alias",
	}
	tightRuled = parityClass{
		code:     codeTightened,
		decision: "Matt, 2026-11-04: refuse leading-zero integers, yes/no booleans and multi-document YAML",
		note:     "yaml.v3 reads 010 as octal 8, yes as true, and only the first document",
	}
	gatedC = parityClass{
		code:     codeGated,
		decision: "C: unknown keys stay warn-only, strict admission opt-in",
		waitsOn:  "Ashlar card yamlconfig-report-unknown-fields (collect every unknown key with its location while decoding the known ones)",
		note:     "v0.4.0 DecodeStrict stops at the first unknown key, so this config would not load; the harness emulates collect mode",
	}
	gatedEmptyEnv = parityClass{
		code:     codeGated,
		decision: "G (empty-env prerequisite): set-but-empty stays distinct from unset",
		waitsOn:  "Ashlar card yamlconfig-empty-vs-unset-env",
		note:     "Ashlar treats a set-but-empty variable as unresolved; load() substitutes the empty string",
	}
	gatedF = parityClass{
		code:     codeGated,
		decision: "F: ${VAR} in include paths is a compatibility requirement",
		waitsOn:  "no Ashlar card yet: decision F asks for an upstream include-path expansion hook",
		note:     "load() expands the include path; Ashlar looks for the literal path",
	}
	gatedEOrder = parityClass{
		code:     codeGated,
		decision: "E: per-file dotenv ordering is preserved",
		waitsOn:  "slice 4 dotenv adapter (ductile side, no Ashlar card): pin per-file ordering before choosing it",
		note:     "the two-pass sketch makes a later file's dotenv visible to an earlier file; load() interpolates each file before reading later files' dotenv",
	}
	gatedEParse = parityClass{
		code:     codeGated,
		decision: "E: first definition wins and load()'s dotenv parse is kept",
		waitsOn:  "slice 4 dotenv adapter (ductile side, no Ashlar card): parse with loadEnvFile's rules, not yamlconfig.ParseDotEnv",
		note:     "ParseDotEnv keeps the last of a duplicated name and refuses names outside [A-Za-z_][A-Za-z0-9_]*",
	}
	gatedH = parityClass{
		code:     codeGated,
		decision: "H: directory includes stay non-recursive, case-sensitive *.yaml (the card's WIDENED class)",
		waitsOn:  "Ashlar card yamlconfig-directory-include-policy",
		note:     "Ashlar walks recursively and takes .yml and upper-case extensions",
	}
	tightNullOverMapping = parityClass{
		code:     codeTightened,
		decision: "I, ruled by Matt, 2026-11-04: an explicit null over a mapping is rejected (Ashlar's ErrComposition); the operator fixes the file or drops the include",
		note:     "load() ignores the null and keeps the root's mapping. TestParityNullOverMappingNamesBothFiles pins that the error names both file locations",
	}
)

var parityGoldenRows = []goldenRow{
	{"env/dotenv-dashed-name", "full", "env:PARITY-DASHED", "\"x\"", "(not exported)", gatedEParse},
	{"env/dotenv-dashed-name", "full", "outcome", "ok", "ashlar syntax at ", gatedEParse},
	{"env/dotenv-dashed-name", "lenient", "env:PARITY-DASHED", "\"x\"", "(not exported)", gatedEParse},
	{"env/dotenv-dashed-name", "lenient", "outcome", "ok", "ashlar syntax at ", gatedEParse},
	{"env/dotenv-duplicate-name", "full", "env:PARITY_DUP", "\"first\"", "\"second\"", gatedEParse},
	{"env/dotenv-duplicate-name", "full", "field:/TCCPaths/0", "\"first\"", "\"second\"", gatedEParse},
	{"env/dotenv-duplicate-name", "lenient", "env:PARITY_DUP", "\"first\"", "\"second\"", gatedEParse},
	{"env/dotenv-duplicate-name", "lenient", "field:/TCCPaths/0", "\"first\"", "\"second\"", gatedEParse},
	{"env/dotenv-from-later-file", "full", "field:/EnvironmentVars/Include", "null", "[\"late.env\"]", bugDroppedKey},
	{"env/dotenv-from-later-file", "full", "field:/TCCPaths/0", "\"${PARITY_LATE}\"", "\"from-late-file\"", gatedEOrder},
	{"env/dotenv-from-later-file", "lenient", "field:/EnvironmentVars/Include", "null", "[\"late.env\"]", bugDroppedKey},
	{"env/dotenv-from-later-file", "lenient", "field:/TCCPaths/0", "\"${PARITY_LATE}\"", "\"from-late-file\"", gatedEOrder},
	{"env/set-but-empty", "full", "outcome", "ok", "ashlar unresolved at /tcc_paths/0", gatedEmptyEnv},
	{"env/set-but-empty", "lenient", "outcome", "ok", "ashlar unresolved at /tcc_paths/0", gatedEmptyEnv},
	{"env/unresolved-variable", "full", "outcome", "ok", "ashlar unresolved at /tcc_paths/0", tightUnresolved},
	{"env/unresolved-variable", "lenient", "outcome", "ok", "ashlar unresolved at /tcc_paths/0", tightUnresolved},
	{"env/value-with-colon-space", "full", "outcome", "yaml type error: line 10: cannot unmarshal !!map into string", "ok", bugTextInterpolation},
	{"env/value-with-colon-space", "lenient", "outcome", "yaml type error: line 10: cannot unmarshal !!map into string", "ok", bugTextInterpolation},
	{"env/variable-in-include-path", "full", "outcome", "ok", "ashlar not-found at /include/0", gatedF},
	{"env/variable-in-include-path", "lenient", "outcome", "ok", "ashlar not-found at /include/0", gatedF},
	{"fixture/config-view-redaction/config", "full", "unknown-key:/log_level", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"fixture/config-view-redaction/config", "lenient", "unknown-key:/log_level", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"fixture/plugin-crash-leaves-deterministic-state/config", "full", "unknown-key:/log_level", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"fixture/plugin-crash-leaves-deterministic-state/config", "lenient", "unknown-key:/log_level", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"fixture/reload-lifecycle/config", "full", "unknown-key:/log_level", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"fixture/reload-lifecycle/config", "lenient", "unknown-key:/log_level", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"fixture/sys_exec/config", "full", "unknown-key:/log_level", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"fixture/sys_exec/config", "lenient", "unknown-key:/log_level", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"fixture/webhook-ingress/config", "full", "unknown-key:/log_level", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"fixture/webhook-ingress/config", "lenient", "unknown-key:/log_level", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"include/directory", "lenient", "field:/Routes", "[{\"EventType\":\"yaml\",\"From\":\"a\",\"To\":\"b\"}]", "[{\"EventType\":\"upper\",\"From\":\"a\",\"To\":\"b\"},{\"EventType\":\"yaml\",\"From\":\"a\",\"To\":\"b\"},{\"EventType\":\"yml\",\"From\":\"a\",\"To\":\"b\"},{\"EventType\":\"nested\",\"From\":\"a\",\"To\":\"b\"}]", gatedH},
	{"include/directory", "lenient", "files", "$CASE/conf.d/a.yaml $CASE/config.yaml", "$CASE/conf.d/UPPER.YAML $CASE/conf.d/a.yaml $CASE/conf.d/b.yml $CASE/conf.d/nested/c.yaml $CASE/config.yaml", gatedH},
	{"include/empty-file", "full", "outcome", "ok", "ashlar syntax at ", tightEmptyDoc},
	{"include/empty-file", "lenient", "outcome", "ok", "ashlar syntax at ", tightEmptyDoc},
	{"literal/loader/797a862fd984", "full", "outcome", "validation: invalid configuration: api.auth.tokens[0]: a literal token value is not allowed — API secrets live in the vault; use secret_ref (ADR §8.5, #94)", "ashlar unresolved at /api/auth/tokens/0/token", tightUnresolved},
	{"literal/loader/797a862fd984", "lenient", "outcome", "ok", "ashlar unresolved at /api/auth/tokens/0/token", tightUnresolved},
	{"literal/loader/ad8f3494e733", "full", "outcome", "validation: invalid configuration: plugin \"test\": environment variable ${…} is not set", "ashlar unresolved at /state/path", tightUnresolved},
	{"literal/loader/ad8f3494e733", "lenient", "outcome", "ok", "ashlar unresolved at /state/path", tightUnresolved},
	{"literal/loader/caeed33c2ab5", "full", "outcome", "validation: invalid configuration: api.auth.tokens[0]: a literal token value is not allowed — API secrets live in the vault; use secret_ref (ADR §8.5, #94)", "ashlar unresolved at /api/auth/tokens/0/token", tightUnresolved},
	{"literal/loader/caeed33c2ab5", "lenient", "outcome", "ok", "ashlar unresolved at /api/auth/tokens/0/token", tightUnresolved},
	{"literal/loader/fc6b03866b59", "full", "outcome", "validation: invalid configuration: plugin \"test\": environment variable ${…} is not set", "ashlar unresolved at /plugins/test/config/secret_ref", tightUnresolved},
	{"literal/loader/fc6b03866b59", "lenient", "outcome", "ok", "ashlar unresolved at /plugins/test/config/secret_ref", tightUnresolved},
	{"merge/include-dropped-keys", "full", "field:/Accounts", "null", "{\"default\":{\"GID\":4181,\"Home\":\"\",\"StateDir\":\"$CASE/accounts/default\",\"UID\":4181}}", bugDroppedKey},
	{"merge/include-dropped-keys", "full", "field:/Database/Path", "\"\"", "\"./other.db\"", bugDroppedKey},
	{"merge/include-dropped-keys", "full", "field:/Secrets/VaultFile", "\"\"", "\"other-vault.age\"", bugDroppedKey},
	{"merge/include-dropped-keys", "full", "field:/Service/Admission", "null", "{\"FailOnDrift\":true,\"FailOnSideDoor\":false,\"RequireAPIAuth\":false,\"ValidateConfigOnBoot\":false,\"VerifyIntegrityOnBoot\":true}", bugDroppedKey},
	{"merge/include-dropped-keys", "full", "field:/Service/AllowSymlinks", "false", "true", bugDroppedKey},
	{"merge/include-dropped-keys", "full", "field:/Service/HookMaxDepth", "4", "6", bugDroppedKey},
	{"merge/include-dropped-keys", "full", "field:/Service/MaxWorkers", "2", "3", bugDroppedKey},
	{"merge/include-dropped-keys", "full", "field:/Service/PluginEnvPassthrough", "null", "[\"PARITY_PASS\"]", bugDroppedKey},
	{"merge/include-dropped-keys", "full", "field:/Service/StrictMode", "false", "true", bugDroppedKey},
	{"merge/include-dropped-keys", "full", "field:/Service/Unconfined", "false", "true", bugDroppedKey},
	{"merge/include-dropped-keys", "full", "field:/TCCPaths", "null", "[\"$CASE/tcc\"]", bugDroppedKey},
	{"merge/include-dropped-keys", "lenient", "field:/Accounts", "null", "{\"default\":{\"GID\":4181,\"Home\":\"\",\"StateDir\":\"$CASE/accounts/default\",\"UID\":4181}}", bugDroppedKey},
	{"merge/include-dropped-keys", "lenient", "field:/Database/Path", "\"\"", "\"./other.db\"", bugDroppedKey},
	{"merge/include-dropped-keys", "lenient", "field:/Secrets/VaultFile", "\"\"", "\"other-vault.age\"", bugDroppedKey},
	{"merge/include-dropped-keys", "lenient", "field:/Service/Admission", "null", "{\"FailOnDrift\":true,\"FailOnSideDoor\":false,\"RequireAPIAuth\":false,\"ValidateConfigOnBoot\":false,\"VerifyIntegrityOnBoot\":true}", bugDroppedKey},
	{"merge/include-dropped-keys", "lenient", "field:/Service/AllowSymlinks", "false", "true", bugDroppedKey},
	{"merge/include-dropped-keys", "lenient", "field:/Service/HookMaxDepth", "4", "6", bugDroppedKey},
	{"merge/include-dropped-keys", "lenient", "field:/Service/MaxWorkers", "2", "3", bugDroppedKey},
	{"merge/include-dropped-keys", "lenient", "field:/Service/PluginEnvPassthrough", "null", "[\"PARITY_PASS\"]", bugDroppedKey},
	{"merge/include-dropped-keys", "lenient", "field:/Service/StrictMode", "false", "true", bugDroppedKey},
	{"merge/include-dropped-keys", "lenient", "field:/Service/Unconfined", "false", "true", bugDroppedKey},
	{"merge/include-dropped-keys", "lenient", "field:/TCCPaths", "null", "[\"$CASE/tcc\"]", bugDroppedKey},
	{"merge/include-false-replaces-true", "full", "field:/RemoteIngress/RequireKeyID", "true", "false", bugNonZeroWins},
	{"merge/include-false-replaces-true", "lenient", "field:/RemoteIngress/RequireKeyID", "true", "false", bugNonZeroWins},
	{"merge/include-turns-api-off", "full", "outcome", "validation: invalid configuration: api.auth.tokens must be configured when API is enabled (or genesis a vault to bootstrap one — credential-ladder ADR)", "ok", bugNonZeroWins},
	{"merge/include-turns-api-off", "lenient", "field:/API/Enabled", "true", "false", bugNonZeroWins},
	{"merge/null-plugins-in-include", "full", "outcome", "ok", "ashlar composition at /plugins", tightNullOverMapping},
	{"merge/null-plugins-in-include", "lenient", "outcome", "ok", "ashlar composition at /plugins", tightNullOverMapping},
	{"posture/accounts-in-include", "full", "field:/Accounts", "null", "{\"default\":{\"GID\":4181,\"Home\":\"\",\"StateDir\":\"$CASE/accounts/default\",\"UID\":4181},\"untrusted\":{\"GID\":4182,\"Home\":\"\",\"StateDir\":\"$CASE/accounts/untrusted\",\"UID\":4182}}", bugDroppedKey},
	{"posture/accounts-in-include", "lenient", "field:/Accounts", "null", "{\"default\":{\"GID\":4181,\"Home\":\"\",\"StateDir\":\"$CASE/accounts/default\",\"UID\":4181},\"untrusted\":{\"GID\":4182,\"Home\":\"\",\"StateDir\":\"$CASE/accounts/untrusted\",\"UID\":4182}}", bugDroppedKey},
	{"posture/unconfined-in-include", "full", "field:/Service/Unconfined", "false", "true", bugDroppedKey},
	{"posture/unconfined-in-include", "lenient", "field:/Service/Unconfined", "false", "true", bugDroppedKey},
	{"syntax/anchor-and-alias", "full", "outcome", "ok", "ashlar syntax at /plugins/two", tightAlias},
	{"syntax/anchor-and-alias", "lenient", "outcome", "ok", "ashlar syntax at /plugins/two", tightAlias},
	{"syntax/leading-zero-integer", "full", "outcome", "ok", "ashlar syntax at /plugins/echo/parallelism", tightRuled},
	{"syntax/leading-zero-integer", "lenient", "outcome", "ok", "ashlar syntax at /plugins/echo/parallelism", tightRuled},
	{"syntax/second-document", "full", "outcome", "ok", "ashlar trailing-content at ", tightRuled},
	{"syntax/second-document", "lenient", "outcome", "ok", "ashlar trailing-content at ", tightRuled},
	{"syntax/unknown-key", "full", "unknown-key:/log_level", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"syntax/unknown-key", "full", "unknown-key:/service_typo", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"syntax/unknown-key", "lenient", "unknown-key:/log_level", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"syntax/unknown-key", "lenient", "unknown-key:/service_typo", "ignored", "collected (v0.4.0 DecodeStrict refuses)", gatedC},
	{"syntax/yaml11-boolean", "full", "outcome", "ok", "ashlar type at /plugins/echo/enabled", tightRuled},
	{"syntax/yaml11-boolean", "lenient", "outcome", "ok", "ashlar type at /plugins/echo/enabled", tightRuled},
}
