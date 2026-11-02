package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/mattjoyce/ashlar/yamlconfig"
)

// expandTilde expands a leading "~/" (or a bare "~") to the user's home
// directory through Ashlar (yamlconfig.ExpandPath). A path that does not start
// with "~" is returned untouched, so relative paths keep their later
// config-dir-relative join.
//
// The "~user" form is refused with an error: half-supporting it would be worse
// than refusing it, and leaving it untouched used to create a directory named
// "~user" under the config dir. When the home directory cannot be determined
// the path is returned unchanged, keeping the pre-expansion behaviour
// (config-dir-relative join) as the fallback; a "~user" path then goes through
// unchanged too, because no expansion is attempted.
func expandTilde(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	if home, err := os.UserHomeDir(); err != nil || home == "" {
		return path, nil
	}
	expanded, err := yamlconfig.ExpandPath(path)
	if err != nil {
		return "", err
	}
	return expanded, nil
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
