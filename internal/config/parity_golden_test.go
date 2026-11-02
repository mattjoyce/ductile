package config

// parityGolden is the reviewed list of every difference between load() and
// loadAshlar() over the parity corpus (card 153 slice 3). Key:
// "<case id> <mode> <difference>". Codes follow the card and its 2026-11-01
// decision note:
//
//   - BUG-FIXED: a "Problem now" bug in load() that the Ashlar path does not
//     have. Each has a check in parityBugChecks that fails against load().
//   - TIGHTENED: a decision-G tightening. Unresolved ${VAR} failing closed is
//     accepted outright; empty documents and aliases are accepted only after
//     the live audit and a documented migration.
//   - WIDENED: decision H. The decision is to keep today's non-recursive,
//     case-sensitive .yaml directory includes, so every WIDENED entry blocks
//     the cutover until Ashlar exposes an enumeration policy.
//   - UNDECIDED-pending: not classifiable with the four codes from the
//     decision note. Each note names the item it belongs to. Reported to the
//     reviewer; not a failure until it is given a code or UNDECIDED.

var parityGolden = buildParityGolden()

const (
	noteDroppedKey = "Problem now: deepMergeConfig never copies this key from an include; load() keeps the root's value or the zero value"
	noteUnresolved = "G: an unresolved ${VAR} fails closed with file:line:column; load() keeps the literal placeholder (the literal's test sets the variable; the harness does not)"
	noteUnknownKey = "C: decided to keep unknown keys warn-only, and Ashlar v0.4.0 has no collect mode (DecodeStrict stops at the first unknown key, so this config would not load). The harness emulates collect mode; the code waits on the Ashlar card"
)

func buildParityGolden() map[string]parityEntry {
	g := make(map[string]parityEntry)
	add := func(caseID, modes, diff string, code parityCode, note string) {
		for _, m := range []string{"lenient", "full"} {
			if modes == "both" || modes == m {
				g[caseID+" "+m+" "+diff] = parityEntry{code: code, note: note}
			}
		}
	}

	// BUG-FIXED: the include merge drops keys (Problem now, first bullet).
	for _, field := range []string{
		"/Accounts", "/Database/Path", "/Secrets/VaultFile", "/Service/Admission",
		"/Service/AllowSymlinks", "/Service/HookMaxDepth", "/Service/MaxWorkers",
		"/Service/PluginEnvPassthrough", "/Service/StrictMode", "/Service/Unconfined", "/TCCPaths",
	} {
		add("merge/include-dropped-keys", "both", "field:"+field, codeBugFixed, noteDroppedKey)
	}
	add("posture/accounts-in-include", "both", "field:/Accounts", codeBugFixed, noteDroppedKey+"; the privsep boot gate then sees no accounts")
	add("posture/unconfined-in-include", "both", "field:/Service/Unconfined", codeBugFixed, noteDroppedKey+"; the privsep boot gate then sees no override")
	add("env/dotenv-from-later-file", "both", "field:/EnvironmentVars/Include", codeBugFixed, noteDroppedKey)

	// BUG-FIXED: non-zero wins (Problem now, second bullet).
	add("merge/include-turns-api-off", "lenient", "field:/API/Enabled", codeBugFixed,
		"Problem now: an include can turn the API on but never off; decision I: a present false replaces")
	add("merge/include-turns-api-off", "full", "outcome:legacy-error", codeBugFixed,
		"Problem now: load() keeps api.enabled true from the root, then refuses an enabled API with no token")
	add("merge/include-false-replaces-true", "both", "field:/RemoteIngress/RequireKeyID", codeBugFixed,
		"Problem now: remote_ingress.require_key_id can be turned on by an include but never off")

	// BUG-FIXED: interpolation is text substitution (Problem now, third bullet).
	add("env/value-with-colon-space", "both", "outcome:legacy-error", codeBugFixed,
		"Problem now: a value containing ': ' changes document structure under text substitution; Ashlar interpolates the scalar")

	// TIGHTENED (G).
	add("env/unresolved-variable", "both", "outcome:ashlar-error:unresolved:/tcc_paths/0", codeTightened, noteUnresolved)
	add("literal/loader/797a862fd984", "lenient", "outcome:ashlar-error:unresolved:/api/auth/tokens/0/token", codeTightened, noteUnresolved)
	add("literal/loader/caeed33c2ab5", "lenient", "outcome:ashlar-error:unresolved:/api/auth/tokens/0/token", codeTightened, noteUnresolved)
	add("literal/loader/ad8f3494e733", "lenient", "outcome:ashlar-error:unresolved:/state/path", codeTightened, noteUnresolved)
	add("literal/loader/fc6b03866b59", "lenient", "outcome:ashlar-error:unresolved:/plugins/test/config/secret_ref", codeTightened, noteUnresolved)
	add("include/empty-file", "both", "outcome:ashlar-error:syntax", codeTightened,
		"G: Ashlar refuses an empty include file that load() reads as nothing; accepted only after the live audit")
	add("syntax/anchor-and-alias", "both", "outcome:ashlar-error:syntax:/plugins/two", codeTightened,
		"G: Ashlar refuses YAML aliases; accepted only after the live audit")

	// WIDENED (H).
	add("include/directory", "lenient", "field:/Routes", codeWidened,
		"H: Ashlar walks the directory recursively and takes .yml and upper-case extensions; load() takes top-level *.yaml only. Decision H keeps today's rule, so this blocks the cutover")
	add("include/directory", "lenient", "files", codeWidened,
		"H: the same widening seen as extra resolved files (b.yml, UPPER.YAML, nested/c.yaml)")

	// UNDECIDED-pending.
	for _, fixture := range []string{"config-view-redaction", "plugin-crash-leaves-deterministic-state", "reload-lifecycle", "sys_exec", "webhook-ingress"} {
		add("fixture/"+fixture+"/config", "both", "unknown-key:/log_level", codePending, noteUnknownKey)
	}
	add("syntax/unknown-key", "both", "unknown-key:/log_level", codePending, noteUnknownKey)
	add("syntax/unknown-key", "both", "unknown-key:/service_typo", codePending, noteUnknownKey)
	add("env/set-but-empty", "both", "outcome:ashlar-error:unresolved:/tcc_paths/0", codePending,
		"empty-env prerequisite: Ashlar treats a set-but-empty variable as unresolved; the note requires set-empty and unset to stay distinct")
	add("env/variable-in-include-path", "both", "outcome:ashlar-error:not-found:/include/0", codePending,
		"F: ${VAR} in an include path is a compatibility requirement; Ashlar v0.4.0 does not expand it")
	add("env/dotenv-from-later-file", "both", "field:/TCCPaths/0", codePending,
		"E: the two-pass sketch makes a later file's dotenv visible to an earlier file; load() interpolates each file before reading later files' dotenv")
	add("env/dotenv-duplicate-name", "both", "field:/TCCPaths/0", codePending,
		"E: a name defined twice in one .env keeps its first value in load() and its last in ParseDotEnv")
	add("env/dotenv-duplicate-name", "both", "env:PARITY_DUP", codePending,
		"E: the same duplicate-name difference, seen in the exported environment")
	add("env/dotenv-dashed-name", "both", "outcome:ashlar-error:syntax", codePending,
		"E: ParseDotEnv refuses names outside [A-Za-z_][A-Za-z0-9_]*; load() exports them")
	add("merge/null-plugins-in-include", "both", "outcome:ashlar-error:composition:/plugins", codePending,
		"I: an explicit null over a mapping is a composition error in Ashlar and a no-op in load(); the note asks for null semantics to be tested, not inferred, and records no outcome")
	add("syntax/leading-zero-integer", "both", "outcome:ashlar-error:syntax:/plugins/echo/parallelism", codePending,
		"not in G: Ashlar refuses 010; yaml.v3 reads it as octal 8")
	add("syntax/yaml11-boolean", "both", "outcome:ashlar-error:validation:/plugins/echo/enabled", codePending,
		"not in G: Ashlar refuses yes as a boolean; yaml.v3 reads it as true")
	add("syntax/second-document", "both", "outcome:ashlar-error:trailing-content", codePending,
		"not in G: Ashlar refuses a second YAML document; load() reads the first and ignores the rest")
	return g
}
