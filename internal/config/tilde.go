package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// expandTilde expands a leading "~/" (or a bare "~") to the user's home
// directory. Any other path is returned exactly as written, so relative paths
// keep their later config-dir-relative join. When the home directory cannot be
// determined "~" and "~/..." are returned unchanged, leaving the
// pre-expansion behaviour (config-dir-relative join) as the fallback.
//
// The "~user" form ("~" followed by a POSIX portable username character) is
// refused, whether or not a home directory resolves: half-supporting it would
// be worse than refusing it, and passing it through created a directory named
// "~user" under the config dir (card 153 slice 2, the one approved change).
//
// This body stays ductile's own: yamlconfig.ExpandPath also trims whitespace,
// cleans the result and expands the Windows "~\" form, none of which ductile
// did (card 153 slice 2 fix round).
func expandTilde(path string) (string, error) {
	if isTildeUserForm(path) {
		return "", fmt.Errorf("unsupported ~user home-directory form in path %q; use ~/ or an absolute path", path)
	}
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path, nil
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// isTildeUserForm reports whether path starts with "~" followed by a character
// from the POSIX portable username set [A-Za-z0-9._-].
func isTildeUserForm(path string) bool {
	if len(path) < 2 || path[0] != '~' {
		return false
	}
	c := path[1]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
}

// expandTildeField expands one path field in place, naming the field in any
// error so the operator can find the offending line.
func expandTildeField(field string, value *string) error {
	expanded, err := expandTilde(*value)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	*value = expanded
	return nil
}

// expandTildeList expands every element of a path list in place; errors name
// the field and the index.
func expandTildeList(field string, values []string) error {
	for i := range values {
		if err := expandTildeField(fmt.Sprintf("%s[%d]", field, i), &values[i]); err != nil {
			return err
		}
	}
	return nil
}

// expandTildePaths normalizes every path-bearing config field in place.
// It runs once in load(), after the include merge and before defaults and
// path resolution, so every Load variant (daemon boot, doctor, check, lock,
// lenient status probe) sees the same expanded view. (#143 doc-audit
// follow-up: a "~" in YAML used to be joined under the config dir, an error
// class this removes.) The first "~user" path found fails the load with the
// field named.
func expandTildePaths(cfg *Config) error {
	scalars := []struct {
		field string
		value *string
	}{
		{"state.path", &cfg.State.Path},
		{"database.path", &cfg.Database.Path},
		{"secrets.age_key_file", &cfg.Secrets.AgeKeyFile},
		{"secrets.vault_file", &cfg.Secrets.VaultFile},
		{"api.management_socket", &cfg.API.ManagementSocket},
	}
	for _, s := range scalars {
		if err := expandTildeField(s.field, s.value); err != nil {
			return err
		}
	}
	if err := expandTildeList("plugin_roots", cfg.PluginRoots); err != nil {
		return err
	}
	if err := expandTildeList("tcc_paths", cfg.TCCPaths); err != nil {
		return err
	}
	return expandTildeList("environment_vars.include", cfg.EnvironmentVars.Include)
}
