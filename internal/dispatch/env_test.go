package dispatch

import (
	"strings"
	"testing"
)

// envMap turns a KEY=VALUE slice into a lookup map for assertions.
func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if name, val, ok := strings.Cut(kv, "="); ok {
			m[name] = val
		}
	}
	return m
}

func TestBuildPluginEnvAllowlistsSafeVars(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home/worker")
	t.Setenv("LANG", "en_AU.UTF-8")
	t.Setenv("LC_TIME", "en_AU.UTF-8")
	t.Setenv("TZ", "Australia/Sydney")

	env := envMap(buildPluginEnv(nil))

	for _, name := range []string{"PATH", "HOME", "LANG", "LC_TIME", "TZ"} {
		if _, ok := env[name]; !ok {
			t.Errorf("allowlisted var %s missing from plugin env", name)
		}
	}
	if env["PATH"] != "/usr/bin:/bin" {
		t.Errorf("PATH = %q, want /usr/bin:/bin", env["PATH"])
	}
}

func TestBuildPluginEnvWithholdsSecrets(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("WITHINGS_API_TOKEN", "super-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "another-secret")

	env := envMap(buildPluginEnv(nil))

	if _, leaked := env["WITHINGS_API_TOKEN"]; leaked {
		t.Error("WITHINGS_API_TOKEN leaked into plugin env")
	}
	if _, leaked := env["AWS_SECRET_ACCESS_KEY"]; leaked {
		t.Error("AWS_SECRET_ACCESS_KEY leaked into plugin env")
	}
}

func TestBuildPluginEnvHonoursOperatorPassthrough(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("MY_PLUGIN_FLAG", "on")
	t.Setenv("UNGRANTED", "nope")

	env := envMap(buildPluginEnv([]string{"MY_PLUGIN_FLAG", "  ", ""}))

	if env["MY_PLUGIN_FLAG"] != "on" {
		t.Errorf("granted passthrough MY_PLUGIN_FLAG missing, got %q", env["MY_PLUGIN_FLAG"])
	}
	if _, leaked := env["UNGRANTED"]; leaked {
		t.Error("ungranted var leaked into plugin env")
	}
}

func TestWithAccountRuntimeEnvRebasesHomeAndCache(t *testing.T) {
	const stateDir = "/var/lib/ductile/accounts/default"
	// Base env carries the GATEWAY's home — exactly what must not reach the child.
	base := []string{"PATH=/usr/bin", "HOME=/var/lib/ductile", "TZ=Australia/Sydney"}

	env := envMap(withAccountRuntimeEnv(base, stateDir))

	if env["HOME"] != stateDir {
		t.Errorf("HOME = %q, want account state_dir %q", env["HOME"], stateDir)
	}
	if env["XDG_CACHE_HOME"] != stateDir {
		t.Errorf("XDG_CACHE_HOME = %q, want account state_dir %q", env["XDG_CACHE_HOME"], stateDir)
	}
	// Unrelated allowlisted vars are preserved untouched.
	if env["PATH"] != "/usr/bin" || env["TZ"] != "Australia/Sydney" {
		t.Errorf("non-override vars not preserved: PATH=%q TZ=%q", env["PATH"], env["TZ"])
	}
}

func TestWithAccountRuntimeEnvDoesNotLeakGatewayHome(t *testing.T) {
	const stateDir = "/var/lib/ductile/accounts/untrusted"
	base := []string{"HOME=/var/lib/ductile", "HOME=/some/other", "XDG_CACHE_HOME=/var/lib/ductile/.cache"}

	// Every inherited HOME/XDG_CACHE_HOME entry must be dropped — a single leftover
	// gateway HOME would point a confined child at a dir it cannot write (or read).
	for _, kv := range withAccountRuntimeEnv(base, stateDir) {
		name, val, _ := strings.Cut(kv, "=")
		if (name == "HOME" || name == "XDG_CACHE_HOME") && val != stateDir {
			t.Errorf("inherited %s=%q survived; want only %q", name, val, stateDir)
		}
	}
}
