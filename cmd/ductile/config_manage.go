package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/doctor"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/router/dsl"
	"github.com/mattjoyce/ductile/internal/vault"
	"gopkg.in/yaml.v3"
)

func redactWebhookEndpoint(entry config.WebhookEndpoint) config.WebhookEndpoint {
	return entry
}

func redactWebhookEndpoints(entries []config.WebhookEndpoint) []config.WebhookEndpoint {
	out := make([]config.WebhookEndpoint, len(entries))
	for i, entry := range entries {
		out[i] = redactWebhookEndpoint(entry)
	}
	return out
}

func resolveConfigTarget(configPath, configDir string) (string, string, error) {
	if configPath != "" && configDir != "" {
		return "", "", errors.New("use only one of --config or --config-dir")
	}

	target := configPath
	if configDir != "" {
		target = configDir
	}
	if target == "" {
		discovered, err := config.DiscoverConfigDir()
		if err != nil {
			return "", "", err
		}
		target = discovered
	}

	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", "", err
	}

	info, err := os.Stat(absTarget)
	if err != nil {
		return "", "", fmt.Errorf("config target not found: %w", err)
	}

	if info.IsDir() {
		if _, err := os.Stat(filepath.Join(absTarget, "config.yaml")); err != nil {
			return "", "", fmt.Errorf("config.yaml not found in %s", absTarget)
		}
		return absTarget, absTarget, nil
	}

	return absTarget, filepath.Dir(absTarget), nil
}

func validateConfigAtPath(configPath string) (*doctor.Result, int, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, 1, err
	}
	configPaths, err := config.CollectConfigPaths(configPath, cfg)
	if err != nil {
		return nil, 1, err
	}
	symlinkWarnings, err := config.DetectSymlinks(configPaths)
	if err != nil {
		return nil, 1, err
	}
	for _, warning := range symlinkWarnings {
		fmt.Fprintf(os.Stderr, "WARN: symlink detected (path=%s resolved=%s)\n", warning.Path, warning.Resolved)
	}
	if len(symlinkWarnings) > 0 && !cfg.Service.AllowSymlinks {
		return nil, 1, fmt.Errorf("symlinks detected in config paths but not allowed")
	}

	registry, err := discoverRegistry(cfg, configPath)
	if err != nil {
		return nil, 1, err
	}
	hooks := collectHookProjection(cfg)
	result := doctor.New(cfg, registry).AddHookPipelines(hooks).Validate()
	if !result.Valid {
		return result, 1, nil
	}
	if len(result.Warnings) > 0 {
		return result, 2, nil
	}
	return result, 0, nil
}

// collectHookProjection loads the configured pipeline files and projects them
// into the slim shape doctor.validateHookCycles needs. Pipeline parse errors
// silently yield an empty projection — that's a separate failure surface
// (validateRoutes / loader) and not the doctor cycle-check's concern.
// P2-11.
func collectHookProjection(cfg *config.Config) []doctor.HookPipeline {
	if cfg == nil || len(cfg.SourceFiles) == 0 {
		return nil
	}
	paths := make([]string, 0, len(cfg.SourceFiles))
	for f := range cfg.SourceFiles {
		paths = append(paths, f)
	}
	sort.Strings(paths)
	set, err := dsl.LoadAndCompileFiles(paths)
	if err != nil || set == nil {
		return nil
	}
	hooks := make([]doctor.HookPipeline, 0, len(set.Pipelines))
	for _, pipe := range set.Pipelines {
		if !pipe.IsHook || pipe.Trigger == "" {
			continue
		}
		targets := make([]string, 0, len(pipe.Nodes))
		for _, node := range pipe.Nodes {
			if node.Uses != "" {
				targets = append(targets, node.Uses)
			}
		}
		sort.Strings(targets)
		hooks = append(hooks, doctor.HookPipeline{
			Name:       pipe.Name,
			OnHook:     pipe.Trigger,
			FromPlugin: pipe.FromPlugin,
			Targets:    targets,
		})
	}
	return hooks
}

func discoverRegistry(cfg *config.Config, configPath string) (*plugin.Registry, error) {
	pluginRoots, err := resolvePluginRoots(cfg, configPath)
	if err != nil {
		return nil, err
	}
	registry, err := plugin.DiscoverManyWithOptions(pluginRoots, func(level, msg string, args ...any) {
		if level == "warn" {
			fmt.Fprintf(os.Stderr, "WARN: %s %v\n", msg, args)
		}
	}, plugin.DiscoverOptions{AllowSymlinks: cfg.Service.AllowSymlinks})
	if err != nil {
		return nil, err
	}
	if _, err := plugin.ApplyAliases(registry, cfg.Plugins); err != nil {
		return nil, err
	}
	return registry, nil
}

// resolveConfiguredPluginFingerprints enumerates every configured plugin in
// cfg.Plugins, resolves it through the live plugin registry (honoring aliases),
// and returns a []config.ResolvedPlugin ready for keyed attestation via
// config.ComputePluginFingerprint (used by `ductile plugin lock`).
//
// Configured plugins that do not resolve in any plugin_root produce a hard error:
// the operator cannot attest what the binary will be unable to load.
func resolveConfiguredPluginFingerprints(cfg *config.Config, configPath string) ([]config.ResolvedPlugin, error) {
	if len(cfg.Plugins) == 0 {
		return nil, nil
	}
	pluginRoots, err := resolvePluginRoots(cfg, configPath)
	if err != nil {
		return nil, err
	}
	registry, err := plugin.DiscoverManyWithOptions(
		pluginRoots,
		func(level, msg string, args ...any) {},
		plugin.DiscoverOptions{AllowSymlinks: cfg.Service.AllowSymlinks},
	)
	if err != nil {
		return nil, fmt.Errorf("plugin discovery: %w", err)
	}
	if _, err := plugin.ApplyAliases(registry, cfg.Plugins); err != nil {
		return nil, fmt.Errorf("plugin aliases: %w", err)
	}

	resolved := make([]config.ResolvedPlugin, 0, len(cfg.Plugins))
	var missing []string
	for name, pc := range cfg.Plugins {
		p, ok := registry.Get(name)
		if !ok {
			missing = append(missing, name)
			continue
		}
		resolved = append(resolved, config.ResolvedPlugin{
			Name:           name,
			Enabled:        pc.Enabled,
			Uses:           pc.Uses,
			ManifestPath:   filepath.Join(p.Path, "manifest.yaml"),
			EntrypointPath: p.Entrypoint,
		})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("configured plugins not found in any plugin_root: %s", strings.Join(missing, ", "))
	}
	return resolved, nil
}

// verifyPluginFingerprintsForConfig runs VerifyPluginFingerprints against the
// configured plugin set and live plugin registry for configPath. Returns nil
// when .checksums has no plugin_fingerprints section (downgrade-safe) or when
// recorded fingerprints match current bytes. Returns an error describing hard
// mismatches otherwise. Warnings from VerifyPluginFingerprints are intentionally
// silent here; the caller may upgrade to logger-aware reporting in a future
// revision.
func verifyPluginFingerprintsForConfig(configPath string, owner *vault.Vault) error {
	configDir := resolveConfigDir(configPath)
	manifest, err := config.LoadChecksums(configDir)
	if err != nil {
		return nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config for plugin verify: %w", err)
	}
	if len(manifest.PluginFingerprints) == 0 {
		if len(cfg.Plugins) == 0 {
			return nil
		}
		return fmt.Errorf("plugin fingerprints missing from .checksums; run 'ductile plugin lock --all' to authorize configured plugins")
	}
	pluginRoots, err := resolvePluginRoots(cfg, configPath)
	if err != nil {
		return fmt.Errorf("resolve plugin roots: %w", err)
	}
	registry, err := plugin.DiscoverManyWithOptions(
		pluginRoots,
		func(level, msg string, args ...any) {},
		plugin.DiscoverOptions{AllowSymlinks: cfg.Service.AllowSymlinks},
	)
	if err != nil {
		return fmt.Errorf("plugin discovery for verify: %w", err)
	}
	if _, err := plugin.ApplyAliases(registry, cfg.Plugins); err != nil {
		return fmt.Errorf("plugin aliases for verify: %w", err)
	}
	configured := make(map[string]bool, len(cfg.Plugins))
	current := make(map[string]config.ResolvedPlugin, len(cfg.Plugins))
	for name, pc := range cfg.Plugins {
		configured[name] = pc.Enabled
		p, ok := registry.Get(name)
		if !ok {
			continue
		}
		current[name] = config.ResolvedPlugin{
			Name:           name,
			Enabled:        pc.Enabled,
			Uses:           pc.Uses,
			ManifestPath:   filepath.Join(p.Path, "manifest.yaml"),
			EntrypointPath: p.Entrypoint,
		}
	}
	// Recorded fingerprints are keyed (ADR §3.2, vault-held-or-fail): verification
	// needs the vault nonce. fingerprintNonceForConfig fails closed if no vault is
	// loaded — a present fingerprint set can never be verified unkeyed. owner, when
	// non-nil, is the already-decrypted boot/reload owner: reuse its nonce so verify
	// and delivery share one snapshot (#43 single-decrypt, closes the TOCTOU).
	nonce, err := fingerprintNonceForConfig(configDir, cfg, owner)
	if err != nil {
		return fmt.Errorf("plugin verify: %w", err)
	}
	result := config.VerifyPluginFingerprints(manifest.PluginFingerprints, configured, current, nonce)
	if !result.Passed {
		return fmt.Errorf("plugin fingerprint mismatch: %s", strings.Join(result.Errors, "; "))
	}
	return nil
}

// fingerprintNonceForConfig returns the 32-byte plugin-attestation nonce.
//
// When owner is non-nil it is the vault already decrypted by the one boot/reload
// load (config.LoadWithVault); the nonce is taken from that same in-memory
// snapshot. This is the #43 single-decrypt path: boot and reload no longer
// re-decrypt the age blob just for the nonce, and the nonce that verifies plugin
// fingerprints comes from the identical snapshot that later delivers secrets —
// closing the verify-then-deliver TOCTOU window.
//
// When owner is nil (the CLI 'plugin lock' path, which holds no pre-decrypted
// owner) it falls back to loading the vault for configDir. Either way it is
// fail-closed (Armstrong): no vault to source the nonce from is a hard error —
// attestation is keyed-or-nothing, never a silent downgrade to an unkeyed
// digest. Call this only when there are plugins to attest/verify; a
// zero-fingerprint deployment must not reach here (it opts out of attestation
// and runs vault-less).
func fingerprintNonceForConfig(configDir string, cfg *config.Config, owner *vault.Vault) ([]byte, error) {
	v := owner
	if v == nil {
		var err error
		v, err = config.LoadVault(configDir, cfg)
		if err != nil {
			return nil, err
		}
	}
	if v == nil {
		return nil, fmt.Errorf("plugin attestation requires the vault, but none is loaded for %q (run 'ductile vault init'); attestation is keyed-or-nothing", configDir)
	}
	return v.FingerprintNonce()
}

func resolvePluginRoots(cfg *config.Config, configPath string) ([]string, error) {
	roots := cfg.EffectivePluginRoots()
	if len(roots) == 0 {
		return nil, fmt.Errorf("plugin_roots is required")
	}

	baseDir := configPath
	if cfg.ConfigDir != "" {
		baseDir = cfg.ConfigDir
	} else {
		if info, err := os.Stat(configPath); err == nil && !info.IsDir() {
			baseDir = filepath.Dir(configPath)
		}
	}

	resolved := make([]string, 0, len(roots))
	for _, root := range roots {
		if filepath.IsAbs(root) {
			resolved = append(resolved, root)
			continue
		}
		resolved = append(resolved, filepath.Join(baseDir, root))
	}
	return resolved, nil
}

func printValidationSummary(result *doctor.Result) {
	if result == nil {
		return
	}
	if !result.Valid {
		fmt.Printf("Validation: failed (%d error(s), %d warning(s))\n", len(result.Errors), len(result.Warnings))
		for _, issue := range result.Errors {
			if issue.Field != "" {
				fmt.Printf("  ERROR [%s] %s: %s\n", issue.Category, issue.Field, issue.Message)
			} else {
				fmt.Printf("  ERROR [%s] %s\n", issue.Category, issue.Message)
			}
		}
		for _, issue := range result.Warnings {
			if issue.Field != "" {
				fmt.Printf("  WARN  [%s] %s: %s\n", issue.Category, issue.Field, issue.Message)
			} else {
				fmt.Printf("  WARN  [%s] %s\n", issue.Category, issue.Message)
			}
		}
		return
	}

	if len(result.Warnings) == 0 {
		fmt.Println("Validation: ✓ All checks passed")
		return
	}
	fmt.Printf("Validation: ✓ passed with %d warning(s)\n", len(result.Warnings))
	for _, issue := range result.Warnings {
		if issue.Field != "" {
			fmt.Printf("  WARN  [%s] %s: %s\n", issue.Category, issue.Field, issue.Message)
		} else {
			fmt.Printf("  WARN  [%s] %s\n", issue.Category, issue.Message)
		}
	}
}

func refreshConfigIntegrity(configDir string) error {
	files, err := config.DiscoverConfigFiles(configDir)
	if err != nil {
		_, includeErr := config.GenerateChecksumsWithReport(configDir, []string{"tokens.yaml", "webhooks.yaml"}, false)
		if includeErr != nil {
			return err
		}
		return nil
	}
	return config.GenerateChecksumsFromDiscovery(files, false)
}

func writeFileAtomicWithBackup(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	// #nosec G304 -- config paths are operator-controlled local inputs.
	if current, err := os.ReadFile(path); err == nil {
		// #nosec G703 -- backup path is derived from operator-controlled config path.
		if err := os.WriteFile(path+".bak", current, mode); err != nil {
			return err
		}
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Chmod(mode); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}

func parseFlagsAndPositionals(fs *flag.FlagSet, args []string) ([]string, error) {
	takesValue := make(map[string]bool)
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			return
		}
		takesValue["-"+f.Name] = true
		takesValue["--"+f.Name] = true
	})

	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			if takesValue[arg] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
		} else {
			positionals = append(positionals, arg)
		}
	}

	return positionals, fs.Parse(flags)
}

func runConfigPlugin(args []string) int {
	if len(args) == 0 || isHelpToken(args[0]) {
		printConfigPluginHelp()
		return 0
	}

	action := args[0]
	actionArgs := args[1:]
	switch action {
	case "list":
		return runConfigPluginList(actionArgs)
	case "show":
		return runConfigPluginShow(actionArgs)
	case "set":
		return runConfigPluginSet(actionArgs)
	case "help":
		printConfigPluginHelp()
		return 0
	default:
		// #nosec G705 -- stderr output is plain text, not HTML.
		fmt.Fprintf(os.Stderr, "Unknown config plugin action: %s\n", action)
		return 1
	}
}

func runConfigPluginList(args []string) int {
	var configPath, configDir, format string
	fs := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "", "Path to config file or directory")
	fs.StringVar(&configDir, "config-dir", "", "Path to config directory")
	fs.StringVar(&format, "format", "human", "Output format (human, json)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	resolvedPath, _, err := resolveConfigTarget(configPath, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Resolve config failed: %v\n", err)
		return 1
	}
	cfg, err := config.Load(resolvedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config load error: %v\n", err)
		return 1
	}
	registry, err := discoverRegistry(cfg, resolvedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Plugin discovery error: %v\n", err)
		return 1
	}

	type row struct {
		Name       string   `json:"name"`
		Configured bool     `json:"configured"`
		Commands   []string `json:"commands,omitempty"`
	}
	rows := make([]row, 0, len(registry.All()))
	for name, discovered := range registry.All() {
		cmds := make([]string, 0, len(discovered.Commands))
		for _, c := range discovered.Commands {
			cmds = append(cmds, c.Name+":"+string(c.Type))
		}
		sort.Strings(cmds)
		_, configured := cfg.Plugins[name]
		rows = append(rows, row{Name: name, Configured: configured, Commands: cmds})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	if format == "json" {
		raw, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Println(string(raw))
		return 0
	}

	roots, err := resolvePluginRoots(cfg, resolvedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve plugin roots: %v\n", err)
		return 1
	}
	fmt.Printf("Discovered plugins in %s:\n", strings.Join(roots, ", "))
	if len(rows) == 0 {
		fmt.Println("  (none)")
		return 0
	}
	for _, r := range rows {
		state := "not configured"
		if r.Configured {
			state = "configured"
		}
		fmt.Printf("\n%s (%s)\n", r.Name, state)
		if len(r.Commands) > 0 {
			fmt.Printf("  Commands: %s\n", strings.Join(r.Commands, ", "))
		}
	}
	return 0
}

func runConfigPluginShow(args []string) int {
	var configPath, configDir, format string
	fs := flag.NewFlagSet("plugin show", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "", "Path to config file or directory")
	fs.StringVar(&configDir, "config-dir", "", "Path to config directory")
	fs.StringVar(&format, "format", "human", "Output format (human, json)")
	positionals, err := parseFlagsAndPositionals(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}
	if len(positionals) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: ductile config plugin show <name> [--config PATH] [--config-dir PATH]")
		return 1
	}
	name := positionals[0]

	resolvedPath, _, err := resolveConfigTarget(configPath, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Resolve config failed: %v\n", err)
		return 1
	}
	cfg, err := config.Load(resolvedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config load error: %v\n", err)
		return 1
	}
	pluginCfg, ok := cfg.Plugins[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "Plugin %q not found in config\n", name)
		return 1
	}

	if format == "json" {
		raw, _ := json.MarshalIndent(pluginCfg, "", "  ")
		fmt.Println(string(raw))
		return 0
	}
	raw, _ := yaml.Marshal(pluginCfg)
	fmt.Print(string(raw))
	return 0
}

func runConfigPluginSet(args []string) int {
	var configPath, configDir, format string
	fs := flag.NewFlagSet("plugin set", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "", "Path to config file or directory")
	fs.StringVar(&configDir, "config-dir", "", "Path to config directory")
	fs.StringVar(&format, "format", "human", "Output format (human, json)")
	positionals, err := parseFlagsAndPositionals(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}
	if len(positionals) != 3 {
		fmt.Fprintln(os.Stderr, "Usage: ductile config plugin set <name> <path> <value> [--config PATH] [--config-dir PATH]")
		return 1
	}
	name := positionals[0]
	fieldPath := positionals[1]
	value := positionals[2]

	resolvedPath, resolvedDir, err := resolveConfigTarget(configPath, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Resolve config failed: %v\n", err)
		return 1
	}
	cfg, err := config.Load(resolvedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config load error: %v\n", err)
		return 1
	}

	targetFile, pluginCfg, err := loadOrCreatePluginConfig(cfg, resolvedDir, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve plugin config file: %v\n", err)
		return 1
	}

	pluginMap := map[string]any{}
	encoded, _ := yaml.Marshal(pluginCfg)
	_ = yaml.Unmarshal(encoded, &pluginMap)
	setNestedMapValue(pluginMap, strings.Split(fieldPath, "."), parseScalarValue(value))

	updatedRaw, _ := yaml.Marshal(pluginMap)
	var updated config.PluginConf
	if err := yaml.Unmarshal(updatedRaw, &updated); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid plugin update: %v\n", err)
		return 1
	}

	fileCfg := config.PluginsFileConfig{Plugins: map[string]config.PluginConf{name: updated}}
	raw, err := yaml.Marshal(fileCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to encode plugin file: %v\n", err)
		return 1
	}
	if err := writeFileAtomicWithBackup(targetFile, raw, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write plugin file: %v\n", err)
		return 1
	}
	if err := refreshConfigIntegrity(resolvedDir); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to refresh checksums: %v\n", err)
		return 1
	}

	validation, code, err := validateConfigAtPath(resolvedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Validation failed to run: %v\n", err)
		return 1
	}
	if format == "json" {
		out := map[string]any{
			"plugin":     name,
			"path":       fieldPath,
			"value":      value,
			"updated":    targetFile,
			"validation": validation,
		}
		rawOut, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(rawOut))
		return code
	}

	fmt.Printf("Updated: %s\n", targetFile)
	fmt.Printf("Plugin: %s\n", name)
	fmt.Printf("Changed: %s = %s\n", fieldPath, value)
	printValidationSummary(validation)
	return code
}

func runConfigRoute(args []string) int {
	if len(args) == 0 || isHelpToken(args[0]) {
		printConfigRouteHelp()
		return 0
	}

	action := args[0]
	actionArgs := args[1:]
	switch action {
	case "list":
		return runConfigRouteList(actionArgs)
	case "add":
		return runConfigRouteAdd(actionArgs)
	case "remove":
		return runConfigRouteRemove(actionArgs)
	case "help":
		printConfigRouteHelp()
		return 0
	default:
		// #nosec G705 -- stderr output is plain text, not HTML.
		fmt.Fprintf(os.Stderr, "Unknown config route action: %s\n", action)
		return 1
	}
}

func runConfigRouteList(args []string) int {
	var configPath, configDir, format string
	fs := flag.NewFlagSet("route list", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "", "Path to config file or directory")
	fs.StringVar(&configDir, "config-dir", "", "Path to config directory")
	fs.StringVar(&format, "format", "human", "Output format (human, json)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	_, resolvedDir, err := resolveConfigTarget(configPath, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Resolve config failed: %v\n", err)
		return 1
	}
	routesFile := filepath.Join(resolvedDir, "routes.yaml")
	routesCfg, err := loadRoutesFile(routesFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load routes: %v\n", err)
		return 1
	}

	if format == "json" {
		raw, _ := json.MarshalIndent(routesCfg.Routes, "", "  ")
		fmt.Println(string(raw))
		return 0
	}
	fmt.Printf("Routes in %s:\n", routesFile)
	if len(routesCfg.Routes) == 0 {
		fmt.Println("  (none)")
		return 0
	}
	for _, route := range routesCfg.Routes {
		fmt.Printf("  %s:%s -> %s\n", route.From, route.EventType, route.To)
	}
	return 0
}

func runConfigRouteAdd(args []string) int {
	return runConfigRouteMutation("add", args)
}

func runConfigRouteRemove(args []string) int {
	return runConfigRouteMutation("remove", args)
}

func runConfigRouteMutation(mode string, args []string) int {
	var configPath, configDir, format string
	var from, eventType, to string
	fs := flag.NewFlagSet("route "+mode, flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "", "Path to config file or directory")
	fs.StringVar(&configDir, "config-dir", "", "Path to config directory")
	fs.StringVar(&format, "format", "human", "Output format (human, json)")
	fs.StringVar(&from, "from", "", "Source plugin")
	fs.StringVar(&eventType, "event", "", "Source event type")
	fs.StringVar(&to, "to", "", "Target plugin")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}
	if from == "" || eventType == "" || to == "" {
		fmt.Fprintf(os.Stderr, "Usage: ductile config route %s --from <plugin> --event <event_type> --to <plugin>\n", mode)
		return 1
	}

	resolvedPath, resolvedDir, err := resolveConfigTarget(configPath, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Resolve config failed: %v\n", err)
		return 1
	}
	routesFile := filepath.Join(resolvedDir, "routes.yaml")
	routesCfg, err := loadRoutesFile(routesFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load routes: %v\n", err)
		return 1
	}

	route := config.RouteConfig{From: from, EventType: eventType, To: to}
	switch mode {
	case "add":
		if !containsRoute(routesCfg.Routes, route) {
			routesCfg.Routes = append(routesCfg.Routes, route)
		}
	case "remove":
		filtered := make([]config.RouteConfig, 0, len(routesCfg.Routes))
		removed := false
		for _, existing := range routesCfg.Routes {
			if existing.From == route.From && existing.EventType == route.EventType && existing.To == route.To {
				removed = true
				continue
			}
			filtered = append(filtered, existing)
		}
		if !removed {
			fmt.Fprintf(os.Stderr, "Route not found: %s:%s -> %s\n", from, eventType, to)
			return 1
		}
		routesCfg.Routes = filtered
	}

	if err := writeRoutesFile(routesFile, routesCfg); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write routes file: %v\n", err)
		return 1
	}
	if err := refreshConfigIntegrity(resolvedDir); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to refresh checksums: %v\n", err)
		return 1
	}

	validation, code, err := validateConfigAtPath(resolvedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Validation failed to run: %v\n", err)
		return 1
	}
	if format == "json" {
		out := map[string]any{
			"action":     mode,
			"route":      route,
			"updated":    routesFile,
			"validation": validation,
		}
		rawOut, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(rawOut))
		return code
	}

	actionLabel := "Added"
	if mode == "remove" {
		actionLabel = "Removed"
	}
	fmt.Printf("%s route: %s:%s -> %s\n", actionLabel, from, eventType, to)
	fmt.Printf("Updated: %s\n", routesFile)
	printValidationSummary(validation)
	return code
}

func runConfigWebhook(args []string) int {
	if len(args) == 0 || isHelpToken(args[0]) {
		printConfigWebhookHelp()
		return 0
	}

	action := args[0]
	actionArgs := args[1:]
	switch action {
	case "list":
		return runConfigWebhookList(actionArgs)
	case "add":
		return runConfigWebhookAdd(actionArgs)
	case "help":
		printConfigWebhookHelp()
		return 0
	default:
		// #nosec G705 -- stderr output is plain text, not HTML.
		fmt.Fprintf(os.Stderr, "Unknown config webhook action: %s\n", action)
		return 1
	}
}

func runConfigWebhookList(args []string) int {
	var configPath, configDir, format string
	fs := flag.NewFlagSet("webhook list", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "", "Path to config file or directory")
	fs.StringVar(&configDir, "config-dir", "", "Path to config directory")
	fs.StringVar(&format, "format", "human", "Output format (human, json)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	_, resolvedDir, err := resolveConfigTarget(configPath, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Resolve config failed: %v\n", err)
		return 1
	}
	webhooksFile := filepath.Join(resolvedDir, "webhooks.yaml")
	webhooksCfg, err := loadWebhooksFile(webhooksFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load webhooks: %v\n", err)
		return 1
	}

	if format == "json" {
		// #nosec G117 -- webhook secrets are redacted before marshaling.
		raw, _ := json.MarshalIndent(redactWebhookEndpoints(webhooksCfg.Webhooks), "", "  ")
		fmt.Println(string(raw))
		return 0
	}
	fmt.Printf("Webhooks in %s:\n", webhooksFile)
	if len(webhooksCfg.Webhooks) == 0 {
		fmt.Println("  (none)")
		return 0
	}
	for _, hook := range webhooksCfg.Webhooks {
		name := hook.Name
		if name == "" {
			name = hook.Path
		}
		fmt.Printf("\n%s\n", name)
		fmt.Printf("  Path: %s\n", hook.Path)
		fmt.Printf("  Plugin: %s\n", hook.Plugin)
		fmt.Printf("  SecretRef: %s\n", hook.SecretRef)
	}
	return 0
}

func runConfigWebhookAdd(args []string) int {
	var configPath, configDir, format string
	var name, path, pluginName, secretRef, signatureHeader, maxBodySize string
	fs := flag.NewFlagSet("webhook add", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "", "Path to config file or directory")
	fs.StringVar(&configDir, "config-dir", "", "Path to config directory")
	fs.StringVar(&format, "format", "human", "Output format (human, json)")
	fs.StringVar(&name, "name", "", "Webhook name")
	fs.StringVar(&path, "path", "", "Webhook path (e.g. /webhook/github)")
	fs.StringVar(&pluginName, "plugin", "", "Target plugin")
	fs.StringVar(&secretRef, "secret-ref", "", "Webhook secret reference")
	fs.StringVar(&signatureHeader, "signature-header", "X-Signature-256", "Signature header name")
	fs.StringVar(&maxBodySize, "max-body-size", "1MB", "Maximum request body size")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}
	if path == "" || pluginName == "" {
		fmt.Fprintln(os.Stderr, "Usage: ductile config webhook add --path <path> --plugin <name> [--name <name>] --secret-ref <value>")
		return 1
	}
	if secretRef == "" {
		fmt.Fprintln(os.Stderr, "Error: --secret-ref is required")
		return 1
	}

	resolvedPath, resolvedDir, err := resolveConfigTarget(configPath, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Resolve config failed: %v\n", err)
		return 1
	}
	webhooksFile := filepath.Join(resolvedDir, "webhooks.yaml")
	webhooksCfg, err := loadWebhooksFile(webhooksFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load webhooks: %v\n", err)
		return 1
	}
	for _, existing := range webhooksCfg.Webhooks {
		if existing.Path == path {
			fmt.Fprintf(os.Stderr, "Webhook path %q already exists\n", path)
			return 1
		}
		if name != "" && existing.Name == name {
			fmt.Fprintf(os.Stderr, "Webhook name %q already exists\n", name)
			return 1
		}
	}

	entry := config.WebhookEndpoint{
		Name:            name,
		Path:            path,
		Plugin:          pluginName,
		SecretRef:       secretRef,
		SignatureHeader: signatureHeader,
		MaxBodySize:     maxBodySize,
	}
	webhooksCfg.Webhooks = append(webhooksCfg.Webhooks, entry)
	if err := writeWebhooksFile(webhooksFile, webhooksCfg); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write webhooks file: %v\n", err)
		return 1
	}
	if err := refreshConfigIntegrity(resolvedDir); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to refresh checksums: %v\n", err)
		return 1
	}

	validation, code, err := validateConfigAtPath(resolvedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Validation failed to run: %v\n", err)
		return 1
	}
	if format == "json" {
		out := map[string]any{
			"webhook":    redactWebhookEndpoint(entry),
			"updated":    webhooksFile,
			"validation": validation,
		}
		rawOut, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(rawOut))
		return code
	}

	fmt.Printf("Added webhook: %s\n", path)
	fmt.Printf("Path: %s -> %s\n", path, pluginName)
	fmt.Printf("Updated: %s\n", webhooksFile)
	printValidationSummary(validation)
	return code
}

func runConfigInit(args []string) int {
	var configDir string
	var force bool
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.StringVar(&configDir, "config-dir", "", "Path to config directory")
	fs.BoolVar(&force, "force", false, "Overwrite existing files")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	if configDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to resolve home directory: %v\n", err)
			return 1
		}
		configDir = filepath.Join(homeDir, ".config", "ductile")
	}

	if err := os.MkdirAll(configDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create config directory: %v\n", err)
		return 1
	}
	// #nosec G302 -- directory permissions are enforced separately from file permissions.
	if err := os.Chmod(configDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set permissions on config directory: %v\n", err)
		return 1
	}

	created := []string{}
	writeIfNeeded := func(path, content string, mode os.FileMode) error {
		if _, err := os.Stat(path); err == nil && !force {
			return nil
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			return err
		}
		created = append(created, path)
		return nil
	}

	pluginsDir := filepath.Join(configDir, "plugins")
	scopesDir := filepath.Join(configDir, "scopes")
	for _, dir := range []string{pluginsDir, scopesDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create directory %s: %v\n", dir, err)
			return 1
		}
		// #nosec G302 -- directory permissions are enforced separately from file permissions.
		_ = os.Chmod(dir, 0o700)
	}

	configYAML := `service:
  name: ductile
  tick_interval: 60s
  log_level: info
  log_format: json
state:
  path: ./data/ductile.db
api:
  enabled: false
  listen: "127.0.0.1:8080"
plugin_roots:
  - ` + pluginsDir + `
plugins: {}
`
	if err := writeIfNeeded(filepath.Join(configDir, "config.yaml"), configYAML, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write config.yaml: %v\n", err)
		return 1
	}
	if err := writeIfNeeded(filepath.Join(configDir, "routes.yaml"), "routes: []\n", 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write routes.yaml: %v\n", err)
		return 1
	}
	if err := writeIfNeeded(filepath.Join(configDir, "webhooks.yaml"), "webhooks: []\n", 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write webhooks.yaml: %v\n", err)
		return 1
	}
	// tokens.yaml is no longer scaffolded: the vault is the sole secret source
	// (epic #48). Secrets are added with 'ductile vault set', not a tokens file.

	if err := refreshConfigIntegrity(configDir); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate checksums: %v\n", err)
		return 1
	}

	fmt.Printf("Initialized: %s\n", configDir)
	for _, path := range created {
		fmt.Printf("Created: %s\n", path)
	}
	validation, code, err := validateConfigAtPath(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Validation failed to run: %v\n", err)
		return 1
	}
	printValidationSummary(validation)
	return code
}

func runConfigBackup(args []string) int {
	var configPath, configDir, outputPath string
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "", "Path to config file or directory")
	fs.StringVar(&configDir, "config-dir", "", "Path to config directory")
	fs.StringVar(&outputPath, "output", "", "Backup archive output path")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	_, resolvedDir, err := resolveConfigTarget(configPath, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Resolve config failed: %v\n", err)
		return 1
	}
	if outputPath == "" {
		stamp := time.Now().UTC().Format("2006-01-02T15-04-05Z")
		outputPath = filepath.Join(resolvedDir, "backup-"+stamp+".tar.gz")
	}

	files, err := createConfigBackup(resolvedDir, outputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Backup failed: %v\n", err)
		return 1
	}

	fmt.Printf("Created backup: %s\n", outputPath)
	fmt.Printf("Includes: %s\n", strings.Join(files, ", "))

	// config backup is config-files-only: no database, no vault blob. That makes
	// it a footgun for a vault-bearing system — a restore from it is not a
	// recoverable vault. Steer operators to `system backup` (which includes
	// vault.age + a key-pairing manifest), and warn loudly when a vault blob
	// exists that this archive silently omits.
	if _, err := os.Stat(filepath.Join(resolvedDir, "vault.age")); err == nil {
		fmt.Fprintln(os.Stderr,
			"WARNING: this archive does NOT include the encrypted vault blob (vault.age) or the "+
				"database — it is config files only, so a restore from it is NOT a recoverable vault. "+
				"Use `ductile system backup --scope all` for a complete, recoverable snapshot.")
	} else {
		fmt.Fprintln(os.Stderr,
			"Note: config backup captures config files only (no database, no vault). For a complete "+
				"recoverable snapshot use `ductile system backup`.")
	}
	return 0
}

func runConfigRestore(args []string) int {
	var configPath, configDir string
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "", "Path to config file or directory")
	fs.StringVar(&configDir, "config-dir", "", "Path to config directory")
	positionals, err := parseFlagsAndPositionals(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}
	if len(positionals) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: ductile config restore <archive.tar.gz> [--config-dir PATH]")
		return 1
	}
	archivePath := positionals[0]

	resolvedPath, resolvedDir, err := resolveRestoreTarget(configPath, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Resolve config failed: %v\n", err)
		return 1
	}
	allowSymlinks := false
	if cfg, loadErr := config.Load(resolvedPath); loadErr == nil {
		allowSymlinks = cfg.Service.AllowSymlinks
	} else {
		fmt.Fprintf(os.Stderr, "WARN: unable to load config for symlink policy, defaulting to allow_symlinks=false: %v\n", loadErr)
	}

	if err := restoreConfigBackup(resolvedDir, archivePath, allowSymlinks); err != nil {
		fmt.Fprintf(os.Stderr, "Restore failed: %v\n", err)
		return 1
	}
	if err := refreshConfigIntegrity(resolvedDir); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to refresh checksums: %v\n", err)
		return 1
	}

	validation, code, err := validateConfigAtPath(resolvedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Validation failed to run: %v\n", err)
		return 1
	}
	fmt.Printf("Restored from: %s\n", archivePath)
	printValidationSummary(validation)
	return code
}

func resolveRestoreTarget(configPath, configDir string) (string, string, error) {
	if configPath != "" || configDir != "" {
		if configPath != "" && configDir != "" {
			return "", "", errors.New("use only one of --config or --config-dir")
		}
		target := configPath
		if configDir != "" {
			target = configDir
		}
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", "", err
		}
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			return abs, filepath.Dir(abs), nil
		}
		return abs, abs, nil
	}
	return resolveConfigTarget(configPath, configDir)
}

func loadOrCreatePluginConfig(cfg *config.Config, configDir, name string) (string, config.PluginConf, error) {
	files, err := config.DiscoverConfigFiles(configDir)
	if err == nil {
		for _, path := range files.Plugins {
			// #nosec G304 -- config paths are operator-controlled local inputs.
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return "", config.PluginConf{}, readErr
			}
			fileCfg := config.PluginsFileConfig{}
			if unmarshalErr := yaml.Unmarshal(raw, &fileCfg); unmarshalErr != nil {
				return "", config.PluginConf{}, unmarshalErr
			}
			if pluginCfg, ok := fileCfg.Plugins[name]; ok {
				return path, pluginCfg, nil
			}
		}
	}

	defaultCfg := config.PluginConf{
		Enabled: false,
		Schedules: []config.ScheduleConfig{
			{ID: "default", Every: "daily"},
		},
	}
	if existing, ok := cfg.Plugins[name]; ok {
		defaultCfg = existing
	}

	path := filepath.Join(configDir, "plugins", name+".yaml")
	return path, defaultCfg, nil
}

func parseScalarValue(raw string) any {
	trimmed := strings.TrimSpace(raw)
	lower := strings.ToLower(trimmed)
	if lower == "true" {
		return true
	}
	if lower == "false" {
		return false
	}
	if i, err := strconv.Atoi(trimmed); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return f
	}
	return raw
}

func setNestedMapValue(root map[string]any, path []string, value any) {
	if len(path) == 0 {
		return
	}
	updated := setNestedValue(root, path, value)
	if updatedMap, ok := updated.(map[string]any); ok {
		for k, v := range updatedMap {
			root[k] = v
		}
	}
}

func setNestedValue(current any, path []string, value any) any {
	if len(path) == 0 {
		return current
	}
	segment := strings.TrimSpace(path[0])
	if len(path) == 1 {
		if idx, ok := parseIndex(segment); ok {
			slice := ensureSlice(current, idx)
			slice[idx] = value
			return slice
		}
		m, ok := current.(map[string]any)
		if !ok || m == nil {
			m = map[string]any{}
		}
		m[segment] = value
		return m
	}

	if idx, ok := parseIndex(segment); ok {
		slice := ensureSlice(current, idx)
		slice[idx] = setNestedValue(slice[idx], path[1:], value)
		return slice
	}

	m, ok := current.(map[string]any)
	if !ok || m == nil {
		m = map[string]any{}
	}
	m[segment] = setNestedValue(m[segment], path[1:], value)
	return m
}

func parseIndex(segment string) (int, bool) {
	idx, err := strconv.Atoi(segment)
	if err != nil || idx < 0 {
		return 0, false
	}
	return idx, true
}

func ensureSlice(current any, idx int) []any {
	slice, ok := current.([]any)
	if !ok || slice == nil {
		slice = []any{}
	}
	for len(slice) <= idx {
		slice = append(slice, map[string]any{})
	}
	return slice
}

func containsRoute(routes []config.RouteConfig, target config.RouteConfig) bool {
	for _, route := range routes {
		if route.From == target.From && route.EventType == target.EventType && route.To == target.To {
			return true
		}
	}
	return false
}

func loadRoutesFile(path string) (*config.RoutesFileConfig, error) {
	// #nosec G304 -- config paths are operator-controlled local inputs.
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &config.RoutesFileConfig{Routes: []config.RouteConfig{}}, nil
		}
		return nil, err
	}
	cfg := config.RoutesFileConfig{}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Routes == nil {
		cfg.Routes = []config.RouteConfig{}
	}
	return &cfg, nil
}

func writeRoutesFile(path string, cfg *config.RoutesFileConfig) error {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return writeFileAtomicWithBackup(path, raw, 0o600)
}

func loadWebhooksFile(path string) (*config.WebhooksFileConfig, error) {
	// #nosec G304 -- config paths are operator-controlled local inputs.
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &config.WebhooksFileConfig{Webhooks: []config.WebhookEndpoint{}}, nil
		}
		return nil, err
	}
	cfg := config.WebhooksFileConfig{}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Webhooks == nil {
		cfg.Webhooks = []config.WebhookEndpoint{}
	}
	return &cfg, nil
}

func writeWebhooksFile(path string, cfg *config.WebhooksFileConfig) error {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return writeFileAtomicWithBackup(path, raw, 0o600)
}

const (
	maxConfigBackupEntries  = 2048
	maxConfigBackupBytes    = 50 << 20
	maxConfigBackupFileSize = 10 << 20
)

func createConfigBackup(configDir, outputPath string) ([]string, error) {
	items := []string{"config.yaml", "routes.yaml", "tokens.yaml", "webhooks.yaml", ".checksums", "plugins", "scopes"}
	// #nosec G304 -- output path is operator-controlled local input.
	archiveFile, err := os.Create(outputPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = archiveFile.Close() }()

	gz := gzip.NewWriter(archiveFile)
	defer func() { _ = gz.Close() }()
	tw := tar.NewWriter(gz)
	defer func() { _ = tw.Close() }()

	included := []string{}
	for _, rel := range items {
		abs := filepath.Join(configDir, rel)
		info, err := os.Stat(abs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if info.IsDir() {
			err = filepath.Walk(abs, func(path string, entryInfo os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				relPath, _ := filepath.Rel(configDir, path)
				header, err := tar.FileInfoHeader(entryInfo, "")
				if err != nil {
					return err
				}
				header.Name = filepath.ToSlash(relPath)
				if err := tw.WriteHeader(header); err != nil {
					return err
				}
				if entryInfo.IsDir() {
					return nil
				}
				// config paths are operator-controlled local inputs.
				// #nosec
				file, err := os.Open(path)
				if err != nil {
					return err
				}
				defer func() { _ = file.Close() }()
				_, err = io.Copy(tw, file)
				return err
			})
			if err != nil {
				return nil, err
			}
			included = append(included, rel+"/")
			continue
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil, err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return nil, err
		}
		// #nosec G304 -- config paths are operator-controlled local inputs.
		// #nosec G122 -- config paths are operator-controlled; archives are local operator workflows.
		file, err := os.Open(abs)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(tw, file); err != nil {
			if closeErr := file.Close(); closeErr != nil {
				return nil, closeErr
			}
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		included = append(included, rel)
	}

	sort.Strings(included)
	return included, nil
}

func restoreConfigBackup(configDir, archivePath string, allowSymlinks bool) error {
	// #nosec G304 -- archive path is operator-controlled local input.
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}

	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	var totalBytes int64
	var totalEntries int

	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		totalEntries++
		if totalEntries > maxConfigBackupEntries {
			return fmt.Errorf("archive contains too many entries (max %d)", maxConfigBackupEntries)
		}
		if header.Size < 0 {
			return fmt.Errorf("invalid archive entry size for %s", header.Name)
		}
		if header.Size > maxConfigBackupFileSize {
			return fmt.Errorf("archive entry %s exceeds max size (%d bytes)", header.Name, maxConfigBackupFileSize)
		}
		if totalBytes+header.Size > maxConfigBackupBytes {
			return fmt.Errorf("archive exceeds max size (%d bytes)", maxConfigBackupBytes)
		}
		totalBytes += header.Size

		cleanName := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanName, "..") {
			return fmt.Errorf("invalid archive path: %s", header.Name)
		}
		dest := filepath.Join(configDir, cleanName)
		if !strings.HasPrefix(dest, filepath.Clean(configDir)+string(os.PathSeparator)) && filepath.Clean(dest) != filepath.Clean(configDir) {
			return fmt.Errorf("invalid archive entry destination: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return err
			}
			// destination path is operator-controlled config dir.
			// #nosec
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			limitReader := &io.LimitedReader{R: tr, N: header.Size}
			if _, err := io.Copy(out, limitReader); err != nil {
				if closeErr := out.Close(); closeErr != nil {
					return closeErr
				}
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			if limitReader.N != 0 {
				return fmt.Errorf("archive entry %s truncated", header.Name)
			}
		case tar.TypeSymlink:
			if !allowSymlinks {
				return fmt.Errorf("symlink entry not allowed: %s", header.Name)
			}
			fmt.Fprintf(os.Stderr, "WARN: symlink entry detected in archive: %s -> %s\n", header.Name, header.Linkname)
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, dest); err != nil {
				return err
			}
		case tar.TypeLink:
			if !allowSymlinks {
				return fmt.Errorf("hardlink entry not allowed: %s", header.Name)
			}
			fmt.Fprintf(os.Stderr, "WARN: hardlink entry detected in archive: %s -> %s\n", header.Name, header.Linkname)
			linkTarget := filepath.Join(configDir, filepath.Clean(header.Linkname))
			if !strings.HasPrefix(linkTarget, filepath.Clean(configDir)+string(os.PathSeparator)) && filepath.Clean(linkTarget) != filepath.Clean(configDir) {
				return fmt.Errorf("invalid hardlink target: %s", header.Linkname)
			}
			if err := os.Link(linkTarget, dest); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported archive entry type %q for %s", header.Typeflag, header.Name)
		}
	}
	return nil
}

func printConfigPluginHelp() {
	fmt.Println("Usage: ductile config plugin <action> [flags]")
	fmt.Println("Actions: list, show, set")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  ductile config plugin list")
	fmt.Println("  ductile config plugin show withings")
	fmt.Println("  ductile config plugin set withings schedule.every 2h")
}

func printConfigRouteHelp() {
	fmt.Println("Usage: ductile config route <action> [flags]")
	fmt.Println("Actions: list, add, remove")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  ductile config route list")
	fmt.Println("  ductile config route add --from withings --event weight_updated --to slack")
	fmt.Println("  ductile config route remove --from withings --event weight_updated --to slack")
}

func printConfigWebhookHelp() {
	fmt.Println("Usage: ductile config webhook <action> [flags]")
	fmt.Println("Actions: list, add")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  ductile config webhook list")
	fmt.Println("  ductile config webhook add --path /webhook/github --plugin github-handler --secret '${GITHUB_WEBHOOK_SECRET}'")
}
