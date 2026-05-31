package config

import (
	"bufio"
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mattjoyce/ductile/internal/scheduleexpr"
	"github.com/mattjoyce/ductile/internal/secrets"
	"gopkg.in/yaml.v3"
)

var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads and parses configuration from a file.
// Supports both single-file mode (all config in one file) and multi-file mode (via include array).
func Load(configPath string) (*Config, error) {
	return load(configPath, true, true)
}

// LoadForLock reads configuration for `ductile config lock`. It intentionally
// skips existing .checksums verification so an operator can create or refresh
// the lock manifest from an unlocked state.
func LoadForLock(configPath string) (*Config, error) {
	return load(configPath, false, false)
}

func load(configPath string, verifyScopes bool, validateConfig bool) (*Config, error) {
	// Resolve to absolute path for consistent relative path resolution
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve config path %q: %w", configPath, err)
	}

	// Check if path is directory and resolve config.yaml
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("config file not found: %s\n"+
			"Hint: Check the path or run with --config flag", absPath)
	}

	if info.IsDir() {
		absPath = filepath.Join(absPath, "config.yaml")
		if _, err := os.Stat(absPath); err != nil {
			return nil, fmt.Errorf("directory provided but config.yaml not found: %s", absPath)
		}
	}

	// Resolve the age key before the first read. Only the env var and default
	// locations are available pre-parse — the config's own secrets.age_key_file
	// lives inside the file, so an encrypted root must use one of those.
	configDir := filepath.Dir(absPath)
	kr, err := resolveKeyring(configDir, nil)
	if err != nil {
		return nil, err
	}

	// Load main config file
	cfg, err := loadConfigFile(absPath, make(map[string]bool), kr)
	if err != nil {
		return nil, err
	}
	cfg.SourceFiles = make(map[string]*yaml.Node)

	// If the root config names a key file and we didn't already resolve one
	// (no env var, no default present), honour it for the encrypted includes.
	if kr.Empty() && cfg.Secrets.AgeKeyFile != "" {
		kr, err = resolveKeyring(configDir, cfg)
		if err != nil {
			return nil, err
		}
	}

	// Add root node to SourceFiles (manually since loadConfigFile returns a partial Config)
	rootData, _ := readConfigBytes(kr, absPath)
	var rootNode yaml.Node
	if err := yaml.Unmarshal(rootData, &rootNode); err == nil {
		cfg.SourceFiles[absPath] = &rootNode
	}

	// If include array exists, load and merge included files
	var includedPaths []string
	if len(cfg.Include) > 0 {
		visited := make(map[string]bool)
		// Seed the root config's own path so an include cycle that runs
		// back through the root is detected at the first back-edge —
		// naming the real cycle — instead of after a wasted re-merge of
		// the root file (C-FRO-13: cycle/visited sets must include the
		// origin).
		visited[absPath] = true
		if err := loadIncludes(cfg, cfg.Include, configDir, visited, kr); err != nil {
			return nil, err
		}
		for path := range visited {
			includedPaths = append(includedPaths, path)
		}
	}

	// Apply config defaults before validation
	cfg = applyConfigDefaults(cfg)
	resolveStatePath(cfg, filepath.Dir(absPath))

	if verifyScopes {
		// Hash-verify scope files (tokens.yaml, webhooks.yaml)
		if err := verifyScopeFilesRecursively(includedPaths); err != nil {
			return nil, err
		}
	}

	if validateConfig {
		// Validate configuration (including cross-file references if multi-file mode)
		if len(cfg.Include) > 0 {
			// Multi-file mode: extract tokens for cross-validation
			tokens := extractTokensFromConfig(cfg)
			validator := &ConfigValidator{
				config: cfg,
				tokens: tokens,
			}
			if err := validator.ValidateCrossReferences(); err != nil {
				return nil, fmt.Errorf("configuration validation failed: %w", err)
			}
		}

		// Standard validation
		if err := validate(cfg); err != nil {
			return nil, fmt.Errorf("invalid configuration: %w", err)
		}
	}

	// Apply plugin defaults
	for name, pluginConf := range cfg.Plugins {
		merged := mergePluginDefaults(pluginConf, cfg.Service.MaxWorkers)
		cfg.Plugins[name] = merged
	}

	return cfg, nil
}

// DiscoverConfigDir finds the config directory by checking standard locations.
// Priority order: --config-dir flag, $DUCTILE_CONFIG_DIR, ~/.config/ductile, /etc/ductile.
func DiscoverConfigDir() (string, error) {
	// 1. Check environment variable
	if dir := os.Getenv("DUCTILE_CONFIG_DIR"); dir != "" {
		// #nosec G703 -- config dir is operator-controlled (local operator input).
		if _, err := os.Stat(dir); err == nil {
			return dir, nil
		}
	}

	// 2. Check user config directory
	if homeDir, err := os.UserHomeDir(); err == nil {
		userConfigDir := filepath.Join(homeDir, ".config", "ductile")
		if _, err := os.Stat(userConfigDir); err == nil {
			return userConfigDir, nil
		}
	}

	// 3. Check system config directory
	systemConfigDir := "/etc/ductile"
	if _, err := os.Stat(systemConfigDir); err == nil {
		return systemConfigDir, nil
	}

	return "", fmt.Errorf("no config found (checked: $DUCTILE_CONFIG_DIR, ~/.config/ductile, /etc/ductile)")
}

// DiscoverScopeDirs returns config directories that need .checksums updates.
// It accepts either a config file path or a directory containing config.yaml.
// In include-based mode, it returns directories containing included scope files
// (tokens.yaml, webhooks.yaml). If no scope includes are found, it falls back
// to the root config directory for legacy single-directory behavior.
func DiscoverScopeDirs(configPath string) ([]string, error) {
	absPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve config path %q: %w", configPath, err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("config file not found: %s\nHint: Check the path or run with --config flag", absPath)
	}

	if info.IsDir() {
		absPath = filepath.Join(absPath, "config.yaml")
		if _, err := os.Stat(absPath); err != nil {
			return nil, fmt.Errorf("directory provided but config.yaml not found: %s", absPath)
		}
	}

	configDir := filepath.Dir(absPath)
	kr, err := resolveKeyring(configDir, nil)
	if err != nil {
		return nil, err
	}
	cfg, err := loadConfigFile(absPath, make(map[string]bool), kr)
	if err != nil {
		return nil, err
	}
	cfg.SourceFiles = make(map[string]*yaml.Node)

	if kr.Empty() && cfg.Secrets.AgeKeyFile != "" {
		kr, err = resolveKeyring(configDir, cfg)
		if err != nil {
			return nil, err
		}
	}

	scopeDirs := make(map[string]struct{})
	if len(cfg.Include) > 0 {
		visited := make(map[string]bool)
		visited[absPath] = true // see C-FRO-13 note in load()
		if err := loadIncludes(cfg, cfg.Include, filepath.Dir(absPath), visited, kr); err != nil {
			return nil, err
		}

		for includePath := range visited {
			basename := filepath.Base(includePath)
			if basename == "tokens.yaml" || basename == "webhooks.yaml" {
				scopeDirs[filepath.Dir(includePath)] = struct{}{}
			}
		}
	}

	// Legacy fallback: update root config directory when no scoped include files exist.
	if len(scopeDirs) == 0 {
		scopeDirs[filepath.Dir(absPath)] = struct{}{}
	}

	dirs := make([]string, 0, len(scopeDirs))
	for dir := range scopeDirs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	return dirs, nil
}

// loadIncludes recursively loads and merges files from the include array.
// visited tracks loaded files to prevent cycles.
func loadIncludes(cfg *Config, includes []string, baseDir string, visited map[string]bool, kr *secrets.Keyring) error {
	for i, includePath := range includes {
		// Apply env var interpolation to path
		includePath = interpolateEnv(includePath)

		// Resolve relative paths relative to baseDir
		var resolvedPath string
		if filepath.IsAbs(includePath) {
			resolvedPath = includePath
		} else {
			resolvedPath = filepath.Join(baseDir, includePath)
		}

		// Convert to absolute path for cycle detection
		absPath, err := filepath.Abs(resolvedPath)
		if err != nil {
			return fmt.Errorf("include[%d]: failed to resolve path %q: %w", i, includePath, err)
		}

		info, err := os.Stat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("include[%d]: file not found: %s\n"+
					"Referenced from: %s\n"+
					"Hint: Check the path is correct and the file exists", i, absPath, baseDir)
			}
			return fmt.Errorf("include[%d]: failed to access file %s: %w", i, absPath, err)
		}

		if info.IsDir() {
			files, err := walkDirWithExt(absPath, ".yaml")
			if err != nil {
				return fmt.Errorf("include[%d] (%s): failed to read directory: %w", i, includePath, err)
			}
			for _, file := range files {
				if err := loadIncludeFile(cfg, i, includePath, file, visited, kr); err != nil {
					return err
				}
			}
			continue
		}

		if err := loadIncludeFile(cfg, i, includePath, absPath, visited, kr); err != nil {
			return err
		}
	}

	return nil
}

func loadIncludeFile(cfg *Config, includeIndex int, includePath string, absPath string, visited map[string]bool, kr *secrets.Keyring) error {
	if visited[absPath] {
		return fmt.Errorf("include[%d]: circular dependency detected: %s", includeIndex, absPath)
	}
	visited[absPath] = true

	// Load included file (decrypting if age-encrypted) for source tracking.
	includedData, _ := readConfigBytes(kr, absPath)
	var includedNode yaml.Node
	if err := yaml.Unmarshal(includedData, &includedNode); err == nil {
		cfg.SourceFiles[absPath] = &includedNode
	}

	includedCfg, err := loadConfigFile(absPath, visited, kr)
	if err != nil {
		return fmt.Errorf("include[%d] (%s): %w", includeIndex, includePath, err)
	}

	// Special handling for scope files with non-YAML-serialisable fields
	if filepath.Base(absPath) == "tokens.yaml" {
		if err := graftTokens(cfg, absPath, kr); err != nil {
			return fmt.Errorf("include[%d] (%s): %w", includeIndex, includePath, err)
		}
	}

	// Deep merge included config into main config
	if err := deepMergeConfig(cfg, includedCfg); err != nil {
		return fmt.Errorf("include[%d] (%s): merge failed: %w", includeIndex, includePath, err)
	}

	// If included file has its own includes, recursively load them
	if len(includedCfg.Include) > 0 {
		includedBaseDir := filepath.Dir(absPath)
		if err := loadIncludes(cfg, includedCfg.Include, includedBaseDir, visited, kr); err != nil {
			return err
		}
	}

	return nil
}

func loadEnvIncludes(path string, data []byte, kr *secrets.Keyring) error {
	var envCfg struct {
		EnvironmentVars EnvironmentVarsConfig `yaml:"environment_vars"`
	}
	if err := yaml.Unmarshal(data, &envCfg); err != nil {
		return fmt.Errorf("failed to parse environment_vars from %s: %w", path, err)
	}
	if len(envCfg.EnvironmentVars.Include) == 0 {
		return nil
	}

	baseDir := filepath.Dir(path)
	for i, includePath := range envCfg.EnvironmentVars.Include {
		resolved := interpolateEnv(includePath)
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(baseDir, resolved)
		}
		absPath, err := filepath.Abs(resolved)
		if err != nil {
			return fmt.Errorf("environment_vars.include[%d]: failed to resolve path %q: %w", i, includePath, err)
		}
		if _, err := os.Stat(absPath); err != nil {
			return fmt.Errorf("environment_vars.include[%d]: file not found: %s", i, absPath)
		}
		if err := loadEnvFile(absPath, kr); err != nil {
			return fmt.Errorf("environment_vars.include[%d]: %w", i, err)
		}
	}
	return nil
}

func loadEnvFile(path string, kr *secrets.Keyring) error {
	// Read through the decrypting reader: a .env that holds secrets may be
	// stored age-encrypted, and (per CONFIG_REFERENCE) env files are the
	// recommended home for secrets, so this is exactly what must decrypt.
	data, err := readConfigBytes(kr, path)
	if err != nil {
		return fmt.Errorf("failed to read env file %s: %w", path, err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("invalid env line %d in %s", lineNo, path)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return fmt.Errorf("invalid env line %d in %s", lineNo, path)
		}
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("failed to set env %s from %s: %w", key, path, err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read env file %s: %w", path, err)
	}
	return nil
}

// loadConfigFile loads and parses a single config file.
// visited is used for cycle detection when loading includes (nil for top-level).
func loadConfigFile(path string, visited map[string]bool, kr *secrets.Keyring) (*Config, error) {
	data, err := readConfigBytes(kr, path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	if err := loadEnvIncludes(path, data, kr); err != nil {
		return nil, err
	}

	// Apply environment variable interpolation
	interpolated := interpolateEnv(string(data))

	// Parse YAML into partial config (don't apply defaults yet)
	var cfg Config
	if err := yaml.Unmarshal([]byte(interpolated), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	return &cfg, nil
}

// deepMergeConfig merges src into dst, with src taking precedence for non-zero values.
func deepMergeConfig(dst, src *Config) error {
	// Merge service config (non-zero values from src override dst)
	if src.Service.Name != "" {
		dst.Service.Name = src.Service.Name
	}
	if src.Service.TickInterval != 0 {
		dst.Service.TickInterval = src.Service.TickInterval
	}
	if src.Service.LogLevel != "" {
		dst.Service.LogLevel = src.Service.LogLevel
	}
	if src.Service.LogFormat != "" {
		dst.Service.LogFormat = src.Service.LogFormat
	}
	if src.Service.DedupeTTL != 0 {
		dst.Service.DedupeTTL = src.Service.DedupeTTL
	}
	if src.Service.JobLogRetention != 0 {
		dst.Service.JobLogRetention = src.Service.JobLogRetention
	}
	if src.Service.JobQueueRetention != 0 {
		dst.Service.JobQueueRetention = src.Service.JobQueueRetention
	}
	if src.Service.JobTransitionsRetention != 0 {
		dst.Service.JobTransitionsRetention = src.Service.JobTransitionsRetention
	}
	if src.Service.JobAttemptsRetention != 0 {
		dst.Service.JobAttemptsRetention = src.Service.JobAttemptsRetention
	}
	if src.Service.BreakerTransitionsRetention != 0 {
		dst.Service.BreakerTransitionsRetention = src.Service.BreakerTransitionsRetention
	}

	// Merge state config
	if src.State.Path != "" {
		dst.State.Path = src.State.Path
	}

	// Merge API config
	if src.API.Enabled {
		dst.API.Enabled = src.API.Enabled
	}
	if src.API.Listen != "" {
		dst.API.Listen = src.API.Listen
	}
	if len(src.API.Auth.Tokens) > 0 {
		dst.API.Auth.Tokens = append(dst.API.Auth.Tokens, src.API.Auth.Tokens...)
	}

	// Merge plugin_roots
	if len(src.PluginRoots) > 0 {
		dst.PluginRoots = append(dst.PluginRoots, src.PluginRoots...)
	}

	// Merge plugins (additive - src plugins added/override dst plugins)
	if src.Plugins != nil {
		if dst.Plugins == nil {
			dst.Plugins = make(map[string]PluginConf)
		}
		maps.Copy(dst.Plugins, src.Plugins)
	}

	// Merge routes (append)
	if len(src.Routes) > 0 {
		dst.Routes = append(dst.Routes, src.Routes...)
	}

	// Merge relay instances (append)
	if len(src.RelayInstances) > 0 {
		dst.RelayInstances = append(dst.RelayInstances, src.RelayInstances...)
	}

	// Merge remote ingress
	if src.RemoteIngress != nil {
		if dst.RemoteIngress == nil {
			dst.RemoteIngress = &RemoteIngressConfig{}
		}
		if src.RemoteIngress.ListenPath != "" {
			dst.RemoteIngress.ListenPath = src.RemoteIngress.ListenPath
		}
		if src.RemoteIngress.MaxBodySize != "" {
			dst.RemoteIngress.MaxBodySize = src.RemoteIngress.MaxBodySize
		}
		if src.RemoteIngress.AllowedClockSkew != 0 {
			dst.RemoteIngress.AllowedClockSkew = src.RemoteIngress.AllowedClockSkew
		}
		if src.RemoteIngress.RequireKeyID {
			dst.RemoteIngress.RequireKeyID = true
		}
		if len(src.RemoteIngress.TrustedPeers) > 0 {
			dst.RemoteIngress.TrustedPeers = append(dst.RemoteIngress.TrustedPeers, src.RemoteIngress.TrustedPeers...)
		}
	}

	// Merge webhooks
	if src.Webhooks != nil {
		if dst.Webhooks == nil {
			dst.Webhooks = &WebhooksConfig{}
		}
		if src.Webhooks.Listen != "" {
			dst.Webhooks.Listen = src.Webhooks.Listen
		}
		if len(src.Webhooks.Endpoints) > 0 {
			dst.Webhooks.Endpoints = append(dst.Webhooks.Endpoints, src.Webhooks.Endpoints...)
		}
	}

	return nil
}

// verifyScopeFilesRecursively verifies hash for any scope files found in the included paths.
// Scope files are auto-detected by basename (tokens.yaml, webhooks.yaml).
func verifyScopeFilesRecursively(paths []string) error {
	// Group paths by directory to avoid loading the same checksums file multiple times
	dirToFiles := make(map[string][]string)
	for _, path := range paths {
		basename := filepath.Base(path)
		if basename == "tokens.yaml" || basename == "webhooks.yaml" {
			dir := filepath.Dir(path)
			dirToFiles[dir] = append(dirToFiles[dir], path)
		}
	}

	for dir, files := range dirToFiles {
		// Load checksums from this directory
		checksums, err := LoadChecksums(dir)
		if err != nil {
			return fmt.Errorf("checksum verification failed in %s: %w\n"+
				"Scope files (tokens.yaml, webhooks.yaml) require hash verification.\n"+
				"Run: ductile config lock --config-dir %s", dir, err, dir)
		}

		// Verify each scope file in this directory
		for _, path := range files {
			absPath, _ := filepath.Abs(path)
			expectedHash, ok := checksums.Hashes[absPath]
			if !ok {
				return fmt.Errorf("scope file %s has no hash in checksums at %s\n"+
					"Run: ductile config lock --config-dir %s", filepath.Base(path), dir, dir)
			}

			if err := VerifyFileHash(path, expectedHash); err != nil {
				return fmt.Errorf("scope file verification failed for %s: %w\n"+
					"This indicates tampering or unauthorized modification.\n"+
					"If you edited this file intentionally, run: ductile config lock --config-dir %s", path, err, dir)
			}
		}
	}

	return nil
}

// extractTokensFromConfig extracts token definitions from config for cross-validation.
func extractTokensFromConfig(cfg *Config) map[string]string {
	m := make(map[string]string, len(cfg.Tokens))
	for _, t := range cfg.Tokens {
		m[t.Name] = t.Key
	}
	return m
}

// applyConfigDefaults merges default values into config where not explicitly set.
func applyConfigDefaults(cfg *Config) *Config {
	defaults := Defaults()

	// Apply service defaults if not set
	if cfg.Service.Name == "" {
		cfg.Service.Name = defaults.Service.Name
	}
	if cfg.Service.TickInterval == 0 {
		cfg.Service.TickInterval = defaults.Service.TickInterval
	}
	if cfg.Service.LogLevel == "" {
		cfg.Service.LogLevel = defaults.Service.LogLevel
	}
	if cfg.Service.LogFormat == "" {
		cfg.Service.LogFormat = defaults.Service.LogFormat
	}
	if cfg.Service.DedupeTTL == 0 {
		cfg.Service.DedupeTTL = defaults.Service.DedupeTTL
	}
	if cfg.Service.JobLogRetention == 0 {
		cfg.Service.JobLogRetention = defaults.Service.JobLogRetention
	}
	if cfg.Service.JobQueueRetention == 0 {
		cfg.Service.JobQueueRetention = defaults.Service.JobQueueRetention
	}
	if cfg.Service.JobTransitionsRetention == 0 {
		cfg.Service.JobTransitionsRetention = defaults.Service.JobTransitionsRetention
	}
	if cfg.Service.JobAttemptsRetention == 0 {
		cfg.Service.JobAttemptsRetention = defaults.Service.JobAttemptsRetention
	}
	if cfg.Service.BreakerTransitionsRetention == 0 {
		cfg.Service.BreakerTransitionsRetention = defaults.Service.BreakerTransitionsRetention
	}
	if cfg.Service.MaxWorkers == 0 {
		cfg.Service.MaxWorkers = defaults.Service.MaxWorkers
	}
	if cfg.Service.HookMaxDepth == 0 {
		cfg.Service.HookMaxDepth = defaults.Service.HookMaxDepth
	}

	// Handle database alias
	if cfg.State.Path == "" && cfg.Database.Path != "" {
		cfg.State.Path = cfg.Database.Path
	}

	// Apply state defaults if not set
	if cfg.State.Path == "" {
		cfg.State.Path = defaults.State.Path
	}

	// Apply API defaults if not set
	if !cfg.API.Enabled && cfg.API.Listen == "" {
		cfg.API = defaults.API
	}
	if cfg.API.MaxConcurrentSync == 0 {
		cfg.API.MaxConcurrentSync = 10
	}
	if cfg.API.MaxSyncTimeout == 0 {
		cfg.API.MaxSyncTimeout = 5 * time.Minute
	}

	return cfg
}

func resolveStatePath(cfg *Config, baseDir string) {
	if cfg == nil {
		return
	}
	if strings.TrimSpace(baseDir) == "" {
		return
	}
	if cfg.State.Path == "" {
		return
	}
	if filepath.IsAbs(cfg.State.Path) {
		return
	}
	cfg.State.Path = filepath.Clean(filepath.Join(baseDir, cfg.State.Path))
}

// interpolateEnv replaces ${VAR} with environment variable values.
// Undefined variables are left as-is (not expanded).
func interpolateEnv(input string) string {
	return envVarPattern.ReplaceAllStringFunc(input, func(match string) string {
		// Extract variable name from ${VAR}
		varName := envVarPattern.FindStringSubmatch(match)[1]

		// Look up environment variable
		if value, exists := os.LookupEnv(varName); exists {
			return value
		}

		// If not found, leave the placeholder (will fail validation if required)
		return match
	})
}

// MinTickInterval is the hard floor for service.tick_interval. Anything below this
// is rejected at config validation as a sanity guard against hostile or accidental
// tight ticks (P2-10): low tick rates can flood the dispatcher work loop with no
// visible cause. Operators who legitimately need finer granularity should redesign
// the workload rather than reduce this floor.
const MinTickInterval = 100 * time.Millisecond

// RecommendedTickInterval is the soft floor (warning, not error). Doctor warns when
// tick_interval is below this value to nudge operators away from chatty service polls.
const RecommendedTickInterval = 1 * time.Second

// validate performs basic validation on the configuration.
func validate(cfg *Config) error {
	// Service validation
	if cfg.Service.TickInterval <= 0 {
		return fmt.Errorf("service.tick_interval must be positive")
	}
	if cfg.Service.TickInterval < MinTickInterval {
		return fmt.Errorf("service.tick_interval %s is below the minimum allowed (%s); see P2-10", cfg.Service.TickInterval, MinTickInterval)
	}
	if cfg.Service.HookMaxDepth < 0 {
		return fmt.Errorf("service.hook_max_depth %d must be >= 0 (0 means use default %d); see P2-11", cfg.Service.HookMaxDepth, DefaultHookMaxDepth)
	}
	if cfg.Service.MaxWorkers <= 0 {
		return fmt.Errorf("service.max_workers must be positive")
	}
	if cfg.Service.DedupeTTL > 0 && cfg.Service.JobQueueRetention > 0 && cfg.Service.JobQueueRetention < cfg.Service.DedupeTTL {
		return fmt.Errorf("service.job_queue_retention (%s) must be >= service.dedupe_ttl (%s) because dedupe checks use terminal rows in job_queue", cfg.Service.JobQueueRetention, cfg.Service.DedupeTTL)
	}

	validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLogLevels[cfg.Service.LogLevel] {
		return fmt.Errorf("service.log_level must be one of: debug, info, warn, error (got %q)", cfg.Service.LogLevel)
	}

	// State validation
	if cfg.State.Path == "" {
		return fmt.Errorf("state.path is required")
	}

	// Plugin roots validation
	if len(cfg.EffectivePluginRoots()) == 0 {
		return fmt.Errorf("plugin_roots is required")
	}

	// API auth validation
	if cfg.API.Enabled {
		if len(cfg.API.Auth.Tokens) == 0 {
			return fmt.Errorf("api.auth.tokens must be configured when API is enabled")
		}
		for i, tok := range cfg.API.Auth.Tokens {
			if tok.Token == "" {
				return fmt.Errorf("api.auth.tokens[%d].token is required", i)
			}
			if envVarPattern.MatchString(tok.Token) {
				matches := envVarPattern.FindStringSubmatch(tok.Token)
				if len(matches) > 1 {
					return fmt.Errorf("api.auth.tokens[%d].token: environment variable ${%s} is not set", i, matches[1])
				}
				return fmt.Errorf("api.auth.tokens[%d].token: unresolved environment variable", i)
			}
			// Empty scope list is valid: token authenticates but passes only
			// scope-free endpoints (e.g. discovery). All operation endpoints
			// require at least one matching scope via requireScopes.
		}
	}

	// Plugin validation
	for name, plugin := range cfg.Plugins {
		if !plugin.Enabled {
			continue // Skip disabled plugins
		}

		if plugin.Schedule != nil {
			return fmt.Errorf("plugin %q: schedule is no longer supported; use schedules[]", name)
		}

		parallelism := plugin.Parallelism
		if parallelism == 0 {
			parallelism = DefaultPluginConf().Parallelism
		}
		if parallelism < 1 {
			return fmt.Errorf("plugin %q: parallelism must be >= 1", name)
		}
		if parallelism > cfg.Service.MaxWorkers {
			return fmt.Errorf("plugin %q: parallelism (%d) cannot exceed service.max_workers (%d)", name, parallelism, cfg.Service.MaxWorkers)
		}

		// Validate schedule entries if present (plugins without schedules are API-triggered only).
		scheduleIDs := make(map[string]struct{}, len(plugin.Schedules))
		for i, schedule := range plugin.NormalizedSchedules() {
			sourcePath := fmt.Sprintf("schedules[%d]", i)
			if err := validateScheduleConfig(name, sourcePath, schedule); err != nil {
				return err
			}

			id := strings.TrimSpace(schedule.ID)
			if _, exists := scheduleIDs[id]; exists {
				return fmt.Errorf("plugin %q: duplicate schedule id %q", name, id)
			}
			scheduleIDs[id] = struct{}{}
		}

		// Check for unresolved env vars in config (security: no secrets leaked in logs)
		if plugin.Config != nil {
			if err := checkUnresolvedEnvVars(plugin.Config, name); err != nil {
				return err
			}
		}
	}

	if err := validateRelayConfig(cfg); err != nil {
		return err
	}

	return nil
}

func validateScheduleConfig(pluginName, sourcePath string, schedule ScheduleConfig) error {
	hasEvery := strings.TrimSpace(schedule.Every) != ""
	hasCron := strings.TrimSpace(schedule.Cron) != ""
	hasAt := strings.TrimSpace(schedule.At) != ""
	hasAfter := schedule.After > 0

	modeCount := 0
	for _, mode := range []bool{hasEvery, hasCron, hasAt, hasAfter} {
		if mode {
			modeCount++
		}
	}
	if modeCount == 0 {
		return fmt.Errorf("plugin %q: %s requires one of every, cron, at, or after", pluginName, sourcePath)
	}
	if modeCount > 1 {
		return fmt.Errorf("plugin %q: %s must set exactly one of every, cron, at, or after", pluginName, sourcePath)
	}

	if hasEvery {
		// Validate schedule.every with flexible parser.
		if _, err := ParseInterval(schedule.Every); err != nil {
			return fmt.Errorf("plugin %q: %w", pluginName, err)
		}
	}
	if hasCron {
		if _, err := scheduleexpr.ParseCron(schedule.Cron); err != nil {
			return fmt.Errorf("plugin %q: invalid %s.cron: %w", pluginName, sourcePath, err)
		}
	}
	if hasAt {
		if _, err := time.Parse(time.RFC3339, schedule.At); err != nil {
			return fmt.Errorf("plugin %q: invalid %s.at %q: expected RFC3339 timestamp", pluginName, sourcePath, schedule.At)
		}
	}
	if schedule.After < 0 {
		return fmt.Errorf("plugin %q: invalid %s.after %q: duration must be positive", pluginName, sourcePath, schedule.After)
	}
	if catchUp := strings.TrimSpace(schedule.CatchUp); catchUp != "" {
		switch catchUp {
		case "skip", "run_once", "run_all":
			// valid
		default:
			return fmt.Errorf("plugin %q: invalid %s.catch_up %q: expected skip, run_once, or run_all", pluginName, sourcePath, schedule.CatchUp)
		}
		if !hasEvery && catchUp != "skip" {
			return fmt.Errorf("plugin %q: %s.catch_up %q is only supported for every schedules", pluginName, sourcePath, catchUp)
		}
	}
	if ifRunning := strings.TrimSpace(schedule.IfRunning); ifRunning != "" {
		switch ifRunning {
		case "skip", "queue", "cancel":
			// valid
		default:
			return fmt.Errorf("plugin %q: invalid %s.if_running %q: expected skip, queue, or cancel", pluginName, sourcePath, schedule.IfRunning)
		}
	}
	if err := validateScheduleConstraints(pluginName, sourcePath, schedule); err != nil {
		return err
	}

	command := strings.TrimSpace(schedule.Command)
	if command == "" {
		command = "poll"
	}
	if command == "handle" {
		return fmt.Errorf("plugin %q: %s.command %q cannot be scheduled", pluginName, sourcePath, command)
	}

	return nil
}

func validateScheduleConstraints(pluginName, sourcePath string, schedule ScheduleConfig) error {
	if tz := strings.TrimSpace(schedule.Timezone); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return fmt.Errorf("plugin %q: invalid %s.timezone %q: %w", pluginName, sourcePath, schedule.Timezone, err)
		}
	}

	if window := strings.TrimSpace(schedule.OnlyBetween); window != "" {
		parts := strings.Split(window, "-")
		if len(parts) != 2 {
			return fmt.Errorf("plugin %q: invalid %s.only_between %q: expected HH:MM-HH:MM", pluginName, sourcePath, schedule.OnlyBetween)
		}
		startMin, err := parseClockMinute(parts[0])
		if err != nil {
			return fmt.Errorf("plugin %q: invalid %s.only_between %q: %w", pluginName, sourcePath, schedule.OnlyBetween, err)
		}
		endMin, err := parseClockMinute(parts[1])
		if err != nil {
			return fmt.Errorf("plugin %q: invalid %s.only_between %q: %w", pluginName, sourcePath, schedule.OnlyBetween, err)
		}
		if startMin == endMin {
			return fmt.Errorf("plugin %q: invalid %s.only_between %q: start and end cannot be equal", pluginName, sourcePath, schedule.OnlyBetween)
		}
	}

	for i, token := range schedule.NotOn {
		if _, err := parseWeekdayToken(token); err != nil {
			return fmt.Errorf("plugin %q: invalid %s.not_on[%d]: %w", pluginName, sourcePath, i, err)
		}
	}

	return nil
}

func parseClockMinute(raw string) (int, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty time")
	}
	parsed, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("expected HH:MM")
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

func parseWeekdayToken(token any) (time.Weekday, error) {
	switch v := token.(type) {
	case int:
		return parseWeekdayInt(v)
	case int64:
		return parseWeekdayInt(int(v))
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("weekday number must be an integer: %v", v)
		}
		return parseWeekdayInt(int(v))
	case string:
		return parseWeekdayString(v)
	default:
		return 0, fmt.Errorf("unsupported type %T (expected weekday name or integer)", token)
	}
}

func parseWeekdayInt(v int) (time.Weekday, error) {
	if v == 7 {
		return time.Sunday, nil
	}
	if v < 0 || v > 6 {
		return 0, fmt.Errorf("weekday number %d out of range [0,6] or 7 for sunday", v)
	}
	return time.Weekday(v), nil
}

func parseWeekdayString(raw string) (time.Weekday, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "sun", "sunday":
		return time.Sunday, nil
	case "mon", "monday":
		return time.Monday, nil
	case "tue", "tues", "tuesday":
		return time.Tuesday, nil
	case "wed", "wednesday":
		return time.Wednesday, nil
	case "thu", "thurs", "thursday":
		return time.Thursday, nil
	case "fri", "friday":
		return time.Friday, nil
	case "sat", "saturday":
		return time.Saturday, nil
	default:
		return 0, fmt.Errorf("unknown weekday %q", raw)
	}
}

// checkUnresolvedEnvVars recursively checks for ${VAR} placeholders in config values.
func checkUnresolvedEnvVars(data map[string]any, pluginName string) error {
	for key, value := range data {
		switch v := value.(type) {
		case string:
			if envVarPattern.MatchString(v) {
				// Extract variable name for better error message
				matches := envVarPattern.FindStringSubmatch(v)
				if len(matches) > 1 {
					return fmt.Errorf("plugin %q: environment variable ${%s} is not set", pluginName, matches[1])
				}
				return fmt.Errorf("plugin %q: unresolved environment variable in config.%s", pluginName, key)
			}
		case map[string]any:
			if err := checkUnresolvedEnvVars(v, pluginName); err != nil {
				return err
			}
		}
	}
	return nil
}

// mergePluginDefaults applies default values to plugin config where not specified.
// maxWorkers is the resolved service.max_workers value, used as the default parallelism.
func mergePluginDefaults(plugin PluginConf, maxWorkers int) PluginConf {
	defaults := DefaultPluginConf()

	if plugin.Retry == nil {
		plugin.Retry = defaults.Retry
	}

	if plugin.Timeouts == nil {
		plugin.Timeouts = defaults.Timeouts
	}

	if plugin.CircuitBreaker == nil {
		plugin.CircuitBreaker = defaults.CircuitBreaker
	}

	if plugin.MaxOutstandingPolls == 0 {
		plugin.MaxOutstandingPolls = defaults.MaxOutstandingPolls
	}
	if plugin.Parallelism == 0 {
		plugin.Parallelism = maxWorkers
	}

	return plugin
}

// ParseInterval converts schedule interval strings to durations.
// Supported formats:
// - Go durations (e.g., "5m", "13h")
// - Extended day/week suffixes (e.g., "3d", "2w")
// - Human aliases ("hourly", "daily", "weekly", "monthly")
func ParseInterval(interval string) (time.Duration, error) {
	normalized := strings.TrimSpace(strings.ToLower(interval))
	if normalized == "" {
		return 0, fmt.Errorf("invalid schedule interval %q: value cannot be empty", interval)
	}

	// Named aliases.
	switch normalized {
	case "hourly":
		return 1 * time.Hour, nil
	case "daily":
		return 24 * time.Hour, nil
	case "weekly":
		return 7 * 24 * time.Hour, nil
	case "monthly":
		// Calendar-aware monthly scheduling is out of MVP scope.
		return 30 * 24 * time.Hour, nil
	}

	// Extended suffixes for days and weeks.
	if strings.HasSuffix(normalized, "d") || strings.HasSuffix(normalized, "w") {
		unit := normalized[len(normalized)-1]
		valueStr := normalized[:len(normalized)-1]
		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid schedule interval %q: %w", interval, err)
		}
		if value <= 0 {
			return 0, fmt.Errorf("schedule interval must be positive: %q", interval)
		}

		var scale time.Duration
		switch unit {
		case 'd':
			scale = 24 * time.Hour
		case 'w':
			scale = 7 * 24 * time.Hour
		default:
			return 0, fmt.Errorf("invalid schedule interval %q", interval)
		}

		d := time.Duration(value * float64(scale))
		if d <= 0 {
			return 0, fmt.Errorf("schedule interval must be positive: %q", interval)
		}
		return d, nil
	}

	// Standard Go duration strings.
	d, err := time.ParseDuration(normalized)
	if err != nil {
		return 0, fmt.Errorf("invalid schedule interval %q: %w", interval, err)
	}

	if d <= 0 {
		return 0, fmt.Errorf("schedule interval must be positive: %q", interval)
	}

	return d, nil
}
