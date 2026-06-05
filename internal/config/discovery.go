package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DiscoverConfigFiles walks a config directory and returns the manifest of discovered files.
// Returns error if config.yaml is missing (hard requirement).
func DiscoverConfigFiles(configDir string) (*ConfigFiles, error) {
	absDir, err := filepath.Abs(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve config dir %q: %w", configDir, err)
	}

	if info, err := os.Stat(absDir); err == nil && !info.IsDir() {
		absDir = filepath.Dir(absDir)
	}

	cf := &ConfigFiles{Root: absDir}

	// config.yaml is mandatory
	configPath := filepath.Join(absDir, "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		return nil, fmt.Errorf("config.yaml not found in %s: %w", absDir, err)
	}
	cf.Config = configPath

	// Optional named files
	if path := filepath.Join(absDir, "webhooks.yaml"); fileExists(path) {
		cf.Webhooks = path
	}
	if path := filepath.Join(absDir, "routes.yaml"); fileExists(path) {
		cf.Routes = path
	}
	if path := filepath.Join(absDir, "relay-instances.yaml"); fileExists(path) {
		cf.RelayInstances = path
	}
	if path := filepath.Join(absDir, "relay-ingress.yaml"); fileExists(path) {
		cf.RelayIngress = path
	}

	// Walk scopes/*.json
	cf.Scopes, err = walkJSONDir(filepath.Join(absDir, "scopes"))
	if err != nil {
		return nil, fmt.Errorf("failed to walk scopes/: %w", err)
	}

	return cf, nil
}

// walkJSONDir returns sorted absolute paths of *.json files in dir.
// Returns nil (not error) if the directory doesn't exist.
func walkJSONDir(dir string) ([]string, error) {
	return walkDirWithExt(dir, ".json")
}

func walkDirWithExt(dir, ext string) ([]string, error) {
	if !dirExists(dir) {
		return nil, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ext) {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
