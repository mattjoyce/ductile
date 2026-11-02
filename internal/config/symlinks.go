package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattjoyce/ashlar/yamlconfig"
)

// SymlinkWarning captures a detected symlink in a config-related path.
type SymlinkWarning struct {
	Path     string
	Resolved string
}

// CollectConfigPaths gathers config-related paths for symlink checks.
func CollectConfigPaths(configPath string, cfg *Config) ([]string, error) {
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve config path %q: %w", configPath, err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat config path %q: %w", absPath, err)
	}

	paths := []string{absPath}

	if info.IsDir() {
		configPath := filepath.Join(absPath, "config.yaml")
		paths = append(paths, configPath)
	} else {
		paths = append(paths, filepath.Dir(absPath))
	}

	if cfg != nil {
		for f := range cfg.SourceFiles {
			paths = append(paths, f)
		}
	}

	seen := make(map[string]struct{}, len(paths))
	unique := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve path %q: %w", path, err)
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		unique = append(unique, abs)
	}
	return unique, nil
}

// DetectSymlinks returns warnings for any paths that resolve through a symlink.
//
// Ashlar (yamlconfig.DetectSymlinks) does the resolution. Two consequences
// callers can rely on: a path that does not exist is skipped rather than
// failing the whole check (a symlink check on a missing file has nothing to
// report; the loader reports the missing file itself), and each warning's
// Path is the cleaned absolute form of the input. The result is never nil, so
// a caller that renders it as JSON sees [] rather than null.
func DetectSymlinks(paths []string) ([]SymlinkWarning, error) {
	found, err := yamlconfig.DetectSymlinks(paths)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve symlinks: %w", err)
	}
	warnings := make([]SymlinkWarning, 0, len(found))
	for _, w := range found {
		warnings = append(warnings, SymlinkWarning{Path: w.Path, Resolved: w.Resolved})
	}
	return warnings, nil
}
