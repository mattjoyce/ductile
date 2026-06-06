package dispatch

import (
	"os"
	"strings"
)

// envAllowlist is the fixed set of environment variable names a plugin child
// inherits. Everything else the gateway holds — including any secret in its own
// environment — is withheld. Secrets reach a plugin only over the stdin request
// channel, never the environment and never argv.
//
// The list is intentionally minimal: enough for a process to find executables
// (PATH), resolve its home, and behave correctly with respect to locale and
// time zone. Anything a specific plugin genuinely needs is added explicitly by
// the operator via service.plugin_env_passthrough.
var envAllowlist = map[string]bool{
	"PATH":     true,
	"HOME":     true,
	"TZ":       true,
	"LANG":     true,
	"LANGUAGE": true,
	"TMPDIR":   true,
	// POSIX locale category variables, enumerated explicitly. We do NOT match
	// an open "LC_" prefix: a precise set cannot be widened by an operator
	// happening to name a secret LC_SOMETHING.
	"LC_ALL":            true,
	"LC_CTYPE":          true,
	"LC_NUMERIC":        true,
	"LC_TIME":           true,
	"LC_COLLATE":        true,
	"LC_MONETARY":       true,
	"LC_MESSAGES":       true,
	"LC_PAPER":          true,
	"LC_NAME":           true,
	"LC_ADDRESS":        true,
	"LC_TELEPHONE":      true,
	"LC_MEASUREMENT":    true,
	"LC_IDENTIFICATION": true,
}

// buildPluginEnv returns the explicit environment for a plugin child: the
// allowlisted variables present in the gateway's environment, plus any extra
// names the operator granted. The result replaces inheritance entirely — the
// caller assigns it to cmd.Env, so a nil/empty gateway value for an allowlisted
// name is simply omitted rather than leaked as empty.
func buildPluginEnv(extra []string) []string {
	extraSet := make(map[string]bool, len(extra))
	for _, name := range extra {
		if name = strings.TrimSpace(name); name != "" {
			extraSet[name] = true
		}
	}

	var env []string
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if envAllowlist[name] || extraSet[name] {
			env = append(env, kv)
		}
	}
	return env
}
