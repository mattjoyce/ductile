package config

import (
	"os"
	"path/filepath"
	"strings"
)

// expandTilde expands a leading "~/" (or a bare "~") to the user's home
// directory. The "~user" form is NOT supported and is returned untouched —
// half-supporting it would be worse than refusing it. When the home
// directory cannot be determined the path is returned unchanged, leaving
// the pre-expansion behavior (config-dir-relative join) as the fallback.
func expandTilde(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// expandTildePaths normalizes every path-bearing config field in place.
// It runs once in load(), after the include merge and before defaults and
// path resolution, so every Load variant (daemon boot, doctor, check, lock,
// lenient status probe) sees the same expanded view. (#143 doc-audit
// follow-up: a "~" in YAML used to be joined under the config dir, an error
// class this removes.)
func expandTildePaths(cfg *Config) {
	cfg.State.Path = expandTilde(cfg.State.Path)
	cfg.Database.Path = expandTilde(cfg.Database.Path)
	cfg.Secrets.AgeKeyFile = expandTilde(cfg.Secrets.AgeKeyFile)
	cfg.Secrets.VaultFile = expandTilde(cfg.Secrets.VaultFile)
	cfg.API.ManagementSocket = expandTilde(cfg.API.ManagementSocket)
	for i, root := range cfg.PluginRoots {
		cfg.PluginRoots[i] = expandTilde(root)
	}
	for i, p := range cfg.TCCPaths {
		cfg.TCCPaths[i] = expandTilde(p)
	}
	for i, p := range cfg.EnvironmentVars.Include {
		cfg.EnvironmentVars.Include[i] = expandTilde(p)
	}
}
