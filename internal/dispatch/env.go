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

// accountRuntimeOverrides are the env names whose values a confined plugin must
// take from its OWN account state_dir rather than inherit from the gateway. HOME
// because a dropped account has no writable home of its own (the gateway's HOME,
// /var/lib/ductile, is 0700 gateway-owned); XDG_CACHE_HOME so cache-using runtimes
// (uv → $XDG_CACHE_HOME/uv) land in the account's private dir, never a shared
// location (the cross-account cache-poisoning vector that sank #109 option A).
var accountRuntimeOverrides = []string{"HOME", "XDG_CACHE_HOME"}

// withAccountRuntimeEnv rebases a confined plugin's runtime onto its account
// state_dir: it drops any inherited HOME/XDG_CACHE_HOME (so the gateway's own home
// never leaks to the child) and re-adds them pointing at stateDir. This is half of
// the #109 runtime-contract fix — privsep gives a dropped account no writable HOME,
// so a plugin that writes anything, or a runtime that needs a cache, failed closed
// until its home was keyed to the 0700 dir it owns. The cwd half (cmd.Dir) is set
// at the spawn site, where the *exec.Cmd lives. Order is deterministic for tests.
func withAccountRuntimeEnv(env []string, stateDir string) []string {
	override := make(map[string]bool, len(accountRuntimeOverrides))
	for _, name := range accountRuntimeOverrides {
		override[name] = true
	}

	out := make([]string, 0, len(env)+len(accountRuntimeOverrides))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if ok && override[name] {
			continue // inherited value dropped; replaced below
		}
		out = append(out, kv)
	}
	for _, name := range accountRuntimeOverrides {
		out = append(out, name+"="+stateDir)
	}
	return out
}

// withCredentialedHome rebases a credentialed (trusted) plugin's HOME onto the
// account's real home so on-disk creds (~/.ssh, ~/.config/gh, the git credential
// helper) resolve. It drops any inherited HOME so the gateway's own home never
// leaks. Unlike withAccountRuntimeEnv (which WALLS a confined plugin to its
// state_dir by also pinning XDG_CACHE_HOME), this sets ONLY HOME and leaves the
// rest of the env — including operator-granted plugin_env_passthrough values —
// untouched, so cache defaults to $HOME/.cache under the real, account-owned home.
func withCredentialedHome(env []string, home string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); ok && name == "HOME" {
			continue // inherited HOME dropped; replaced below
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+home)
}
