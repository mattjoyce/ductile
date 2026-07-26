package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/doctor"
	"github.com/mattjoyce/ductile/internal/fsown"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
	"gopkg.in/yaml.v3"
)

// loadStopwatchSnapshot opens the state DB read-only-ish (storage opens
// SQLite in WAL with shared access), reads the stopwatch snapshot, and
// closes. Returns the snapshot or an error -- caller is responsible
// for treating errors as "skip the check" rather than fatal.
func loadStopwatchSnapshot(dbPath string) (*doctor.StopwatchSnapshot, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("state.path is empty")
	}
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	store := state.NewStore(db)
	rowCount, oldest, err := store.StopwatchSnapshot(context.Background())
	if err != nil {
		return nil, err
	}
	return &doctor.StopwatchSnapshot{
		RowCount:         rowCount,
		OldestRecordedAt: oldest,
	}, nil
}

func runConfigNoun(args []string) int {
	if len(args) < 1 {
		printConfigNounHelp(os.Stderr)
		return 1
	}

	if isHelpToken(args[0]) {
		printConfigNounHelp(os.Stdout)
		return 0
	}

	action := args[0]
	actionArgs := args[1:]

	switch action {
	case "lock":
		if hasHelpFlag(actionArgs) {
			printConfigLockHelp()
			return 0
		}
		return runConfigHashUpdate(actionArgs)
	case "check":
		if hasHelpFlag(actionArgs) {
			printConfigCheckHelp()
			return 0
		}
		return runConfigCheck(actionArgs)
	case "schema":
		if hasHelpFlag(actionArgs) {
			printConfigSchemaHelp()
			return 0
		}
		return runConfigSchema(actionArgs)
	case "validate":
		if hasHelpFlag(actionArgs) {
			printConfigValidateHelp()
			return 0
		}
		return runConfigValidate(actionArgs)
	case "show":
		if hasHelpFlag(actionArgs) {
			printConfigShowHelp()
			return 0
		}
		return runConfigShow(actionArgs)
	case "get":
		if hasHelpFlag(actionArgs) {
			printConfigGetHelp()
			return 0
		}
		return runConfigGet(actionArgs)
	case "set":
		if hasHelpFlag(actionArgs) {
			printConfigSetHelp()
			return 0
		}
		return runConfigSet(actionArgs)
	case "plugin":
		return runConfigPlugin(actionArgs)
	case "route":
		return runConfigRoute(actionArgs)
	case "webhook":
		return runConfigWebhook(actionArgs)
	case "init":
		return runConfigInit(actionArgs)
	case "backup":
		return runConfigBackup(actionArgs)
	case "restore":
		return runConfigRestore(actionArgs)
	case "help":
		printConfigNounHelp(os.Stdout)
		return 0
	default:
		// #nosec G705 -- stderr output is plain text, not HTML.
		fmt.Fprintf(os.Stderr, "Unknown config action: %s\n", action)
		return 1
	}
}

// ... (skipping to action implementations)

func runConfigSet(args []string) int {
	var configPath, configDir string
	var dryRun, apply bool

	fs := flag.NewFlagSet("set", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "", "Path to configuration")
	fs.StringVar(&configDir, "config-dir", "", "Path to configuration directory")
	fs.BoolVar(&dryRun, "dry-run", false, "Preview changes")
	fs.BoolVar(&apply, "apply", false, "Apply changes")

	var kvPair string
	remainingArgs := args
	for len(remainingArgs) > 0 {
		if err := fs.Parse(remainingArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
			return 1
		}
		if fs.NArg() > 0 {
			arg0 := fs.Arg(0)
			if kvPair == "" && strings.Contains(arg0, "=") {
				kvPair = arg0
			}
			remainingArgs = fs.Args()[1:]
		} else {
			remainingArgs = nil
		}
	}

	if kvPair == "" {
		fmt.Fprintf(os.Stderr, "Usage: ductile config set <path>=<value> [--dry-run | --apply]\n")
		return 1
	}

	if !dryRun && !apply {
		fmt.Println("Error: either --dry-run or --apply must be specified for 'config set'.")
		return 1
	}

	parts := strings.SplitN(kvPair, "=", 2)
	path, value := parts[0], parts[1]

	cfg, err := loadConfigForToolWithDir(configPath, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Load error: %v\n", err)
		return 1
	}

	if dryRun {
		// In-memory test without persistence
		err := cfg.SetPath(path, value, false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Dry-run validation failed: %v\n", err)
			return 1
		}
		fmt.Printf("Dry-run: would set %q to %q\n", path, value)
		fmt.Println("Status: Configuration check PASSED.")
		return 0
	}

	// Real application
	if err := cfg.SetPath(path, value, true); err != nil {
		if !strings.Contains(err.Error(), "no valid configuration source found") {
			fmt.Fprintf(os.Stderr, "Apply failed: %v\n", err)
			return 1
		}
		resolvedTarget, _, resolveErr := resolveConfigTarget(configPath, configDir)
		if resolveErr != nil {
			fmt.Fprintf(os.Stderr, "Apply failed: %v\n", err)
			return 1
		}
		if fallbackErr := applyConfigSetFallback(resolvedTarget, path, value); fallbackErr != nil {
			fmt.Fprintf(os.Stderr, "Apply failed: %v\n", fallbackErr)
			return 1
		}
	}

	fmt.Printf("Successfully set %q to %q\n", path, value)
	resolvedTarget, _, err := resolveConfigTarget(configPath, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Validation skipped: %v\n", err)
		return 0
	}
	validation, code, err := validateConfigAtPath(resolvedTarget)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Validation failed to run: %v\n", err)
		return 1
	}
	printValidationSummary(validation)
	return code
}

// ... (skipping to action implementations)

func runConfigShow(args []string) int {
	var configPath, configDir string
	var jsonOut bool

	var effective bool
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	fs.StringVar(&configPath, "config", "", "Path to configuration file or directory")
	fs.StringVar(&configDir, "config-dir", "", "Path to configuration directory")
	fs.BoolVar(&jsonOut, "json", false, "Output in structured JSON format")
	fs.BoolVar(&effective, "effective", false, "Resolve per-plugin defaults and tag each value explicit/default")

	var entity string
	remainingArgs := args
	for len(remainingArgs) > 0 {
		if err := fs.Parse(remainingArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
			return 1
		}
		if fs.NArg() > 0 {
			if entity == "" {
				entity = fs.Arg(0)
			}
			remainingArgs = fs.Args()[1:]
		} else {
			remainingArgs = nil
		}
	}

	if effective {
		cfg, err := loadRawConfigForTool(configPath, configDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Load error: %v\n", err)
			return 1
		}
		return runConfigShowEffective(cfg, entity, jsonOut)
	}

	cfg, err := loadConfigForToolWithDir(configPath, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Load error: %v\n", err)
		return 1
	}

	var result any = cfg
	if entity != "" {
		res, err := cfg.GetPath(entity)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
		result = res
	}

	if jsonOut {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
	} else {
		data, _ := yaml.Marshal(result)
		fmt.Print(string(data))
	}
	return 0
}

func runConfigGet(args []string) int {
	var configPath, configDir string
	var jsonOut bool

	var effective bool
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	fs.StringVar(&configPath, "config", "", "Path to configuration file or directory")
	fs.StringVar(&configDir, "config-dir", "", "Path to configuration directory")
	fs.BoolVar(&jsonOut, "json", false, "Output in structured JSON format")
	fs.BoolVar(&effective, "effective", false, "Resolve a plugin field's effective value and tag it explicit/default")

	var path string
	remainingArgs := args
	for len(remainingArgs) > 0 {
		if err := fs.Parse(remainingArgs); err != nil {
			fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
			return 1
		}
		if fs.NArg() > 0 {
			if path == "" {
				path = fs.Arg(0)
			}
			remainingArgs = fs.Args()[1:]
		} else {
			remainingArgs = nil
		}
	}

	if path == "" {
		fmt.Fprintf(os.Stderr, "Usage: ductile config get <path> [--json] [--effective]\n")
		return 1
	}

	if effective {
		cfg, err := loadRawConfigForTool(configPath, configDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Load error: %v\n", err)
			return 1
		}
		return runConfigGetEffective(cfg, path, jsonOut)
	}

	cfg, err := loadConfigForToolWithDir(configPath, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Load error: %v\n", err)
		return 1
	}

	val, err := cfg.GetPath(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	if jsonOut {
		data, _ := json.MarshalIndent(val, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("%v\n", val)
	}
	return 0
}

func loadConfigForTool(configPath string) (*config.Config, error) {
	return loadConfigForToolWithDir(configPath, "")
}

func loadConfigForToolWithDir(configPath, configDir string) (*config.Config, error) {
	target, err := resolveConfigTargetForTool(configPath, configDir)
	if err != nil {
		return nil, err
	}
	return config.Load(target)
}

// loadRawConfigForTool resolves the --config/--config-dir flags and loads the
// config WITHOUT folding per-plugin defaults (config.LoadRaw), for the effective
// view which needs the raw values to tell explicit from inherited.
func loadRawConfigForTool(configPath, configDir string) (*config.Config, error) {
	target, err := resolveConfigTargetForTool(configPath, configDir)
	if err != nil {
		return nil, err
	}
	return config.LoadRaw(target)
}

// resolveConfigTargetForTool turns the --config/--config-dir flag pair into a
// single config path (discovering the default location when neither is given).
// Split out from loadConfigForToolWithDir so the effective view can feed the same
// resolved target to config.LoadRaw instead of config.Load.
func resolveConfigTargetForTool(configPath, configDir string) (string, error) {
	if configPath != "" && configDir != "" {
		return "", fmt.Errorf("use only one of --config or --config-dir")
	}
	if configDir != "" {
		configPath = configDir
	}
	if configPath == "" {
		discovered, err := config.DiscoverConfigDir()
		if err != nil {
			return "", err
		}
		configPath = discovered
	}
	return configPath, nil
}

// effEntry is one resolved plugin field in display order: its dotted key, the
// value in force, and whether that value was set in the file (explicit) or
// inherited from code defaults (default).
type effEntry struct {
	Key    string
	Value  any
	Source string
}

// effectiveJSONField is the per-field JSON shape for `--effective --json`.
type effectiveJSONField struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
}

// orderedEffectiveFields flattens a resolved PluginConf + its source map into a
// stable, display-ordered list (core fields first, then sorted per-command
// timeout overrides).
func orderedEffectiveFields(eff config.PluginConf, src map[string]string) []effEntry {
	entries := []effEntry{
		{"enabled", eff.Enabled, src["enabled"]},
		{"retry.max_attempts", eff.Retry.MaxAttempts, src["retry.max_attempts"]},
		{"retry.backoff_base", eff.Retry.BackoffBase, src["retry.backoff_base"]},
		{"timeouts.poll", eff.Timeouts.Poll, src["timeouts.poll"]},
		{"timeouts.handle", eff.Timeouts.Handle, src["timeouts.handle"]},
		{"timeouts.health", eff.Timeouts.Health, src["timeouts.health"]},
		{"timeouts.init", eff.Timeouts.Init, src["timeouts.init"]},
		{"circuit_breaker.threshold", eff.CircuitBreaker.Threshold, src["circuit_breaker.threshold"]},
		{"circuit_breaker.reset_after", eff.CircuitBreaker.ResetAfter, src["circuit_breaker.reset_after"]},
		{"max_outstanding_polls", eff.MaxOutstandingPolls, src["max_outstanding_polls"]},
		{"parallelism", eff.Parallelism, src["parallelism"]},
	}
	if len(eff.Timeouts.Overrides) > 0 {
		ovKeys := make([]string, 0, len(eff.Timeouts.Overrides))
		for k := range eff.Timeouts.Overrides {
			ovKeys = append(ovKeys, k)
		}
		sort.Strings(ovKeys)
		for _, k := range ovKeys {
			entries = append(entries, effEntry{"timeouts." + k, eff.Timeouts.Overrides[k], src["timeouts."+k]})
		}
	}
	return entries
}

// effDisplayValue renders durations as their human string (e.g. 5m0s) and
// everything else verbatim, for both the human and JSON effective views.
func effDisplayValue(v any) any {
	if d, ok := v.(time.Duration); ok {
		return d.String()
	}
	return v
}

// effectiveTargetPlugins resolves which configured plugins the effective view
// covers: the named one, or all (sorted) when no entity is given.
func effectiveTargetPlugins(cfg *config.Config, entity string) ([]string, error) {
	if entity != "" {
		if _, ok := cfg.Plugins[entity]; !ok {
			return nil, fmt.Errorf("--effective applies to a configured plugin; %q is not one", entity)
		}
		return []string{entity}, nil
	}
	names := make([]string, 0, len(cfg.Plugins))
	for n := range cfg.Plugins {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// runConfigShowEffective renders the resolved plugin config(s) with per-field
// provenance. cfg must be raw (config.LoadRaw) so explicit vs default is real.
func runConfigShowEffective(cfg *config.Config, entity string, jsonOut bool) int {
	names, err := effectiveTargetPlugins(cfg, entity)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	if jsonOut {
		out := make(map[string]map[string]effectiveJSONField, len(names))
		for _, n := range names {
			eff, src := config.EffectivePluginConf(cfg.Plugins[n], cfg.Service.MaxWorkers)
			fields := make(map[string]effectiveJSONField)
			for _, e := range orderedEffectiveFields(eff, src) {
				fields[e.Key] = effectiveJSONField{Value: effDisplayValue(e.Value), Source: e.Source}
			}
			out[n] = fields
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	for i, n := range names {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("plugin: %s\n", n)
		eff, src := config.EffectivePluginConf(cfg.Plugins[n], cfg.Service.MaxWorkers)
		for _, e := range orderedEffectiveFields(eff, src) {
			fmt.Printf("  %s: %v (%s)\n", e.Key, effDisplayValue(e.Value), e.Source)
		}
	}
	return 0
}

// runConfigGetEffective resolves a single effective plugin value. path is
// <plugin> (whole resolved block) or <plugin>.<field> (e.g. birda.timeouts.handle).
func runConfigGetEffective(cfg *config.Config, path string, jsonOut bool) int {
	parts := strings.SplitN(path, ".", 2)
	name := parts[0]
	if _, ok := cfg.Plugins[name]; !ok {
		fmt.Fprintf(os.Stderr, "Error: --effective applies to a configured plugin; %q is not one\n", name)
		return 1
	}
	// Whole-plugin request: delegate to the show renderer (which resolves once).
	if len(parts) == 1 {
		return runConfigShowEffective(cfg, name, jsonOut)
	}

	eff, src := config.EffectivePluginConf(cfg.Plugins[name], cfg.Service.MaxWorkers)
	field := parts[1]
	for _, e := range orderedEffectiveFields(eff, src) {
		if e.Key != field {
			continue
		}
		if jsonOut {
			data, _ := json.MarshalIndent(effectiveJSONField{Value: effDisplayValue(e.Value), Source: e.Source}, "", "  ")
			fmt.Println(string(data))
		} else {
			fmt.Printf("%v (%s)\n", effDisplayValue(e.Value), e.Source)
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "Error: %q is not an effective field of plugin %q\n", field, name)
	return 1
}

func applyConfigSetFallback(configTarget, path, value string) error {
	configFile := configTarget
	info, err := os.Stat(configTarget)
	if err != nil {
		return err
	}
	if info.IsDir() {
		configFile = filepath.Join(configTarget, "config.yaml")
	}

	// #nosec G304 -- config paths are operator-controlled local inputs.
	original, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}

	var doc map[string]any
	if err := yaml.Unmarshal(original, &doc); err != nil {
		return err
	}
	if doc == nil {
		doc = map[string]any{}
	}

	setNestedMapValue(doc, strings.Split(path, "."), parseScalarValue(value))

	updated, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := writeFileAtomicWithBackup(configFile, updated, 0o600); err != nil {
		return err
	}

	if _, err := config.Load(configTarget); err != nil {
		backupPath := configFile + ".bak"
		// #nosec G304 -- config paths are operator-controlled local inputs.
		if backup, readErr := os.ReadFile(backupPath); readErr == nil {
			// #nosec G703 -- config paths are operator-controlled local inputs.
			_ = os.WriteFile(configFile, backup, 0o600)
		}
		return fmt.Errorf("validation failed: %w", err)
	}
	return nil
}

func isHelpToken(token string) bool {
	return token == "help" || token == "--help" || token == "-h"
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func printConfigNounHelp(w *os.File) {
	_, _ = fmt.Fprintln(w, "Usage: ductile config <action> [flags]")
	_, _ = fmt.Fprintln(w, "Actions: lock, check, schema, validate, show, get, set, plugin, route, webhook, init, backup, restore")
}

func printConfigLockHelp() {
	fmt.Println("Usage: ductile config lock [--config PATH | --config-dir PATH] [-v|--verbose] [--dry-run]")
	fmt.Println("Authorize the config files by regenerating their integrity hashes. Recorded plugin")
	fmt.Println("attestations are preserved (de-configured ones pruned); use 'ductile plugin lock' to")
	fmt.Println("(re-)attest a plugin's bytes.")
}

func printConfigCheckHelp() {
	fmt.Println("Usage: ductile config check [--config PATH | --config-dir PATH] [--format human|json] [--strict] [--json]")
	fmt.Println("Validate configuration syntax, policy, and integrity.")
}

func printConfigShowHelp() {
	fmt.Println("Usage: ductile config show [entity] [--config PATH | --config-dir PATH] [--json] [--effective]")
	fmt.Println("Show full resolved configuration or a filtered entity node.")
	fmt.Println("--effective [plugin]: render each plugin's in-force values (folding code defaults)")
	fmt.Println("  with every field tagged (explicit) when file-set or (default) when inherited.")
}

func printConfigGetHelp() {
	fmt.Println("Usage: ductile config get <path> [--config PATH | --config-dir PATH] [--json] [--effective]")
	fmt.Println("Read a single value from the resolved configuration.")
	fmt.Println("--effective <plugin>[.<field>]: resolve a plugin's in-force value(s) with explicit/default tags")
	fmt.Println("  e.g. ductile config get --effective birda.timeouts.handle")
}

func printConfigSetHelp() {
	fmt.Println("Usage: ductile config set <path>=<value> [--config PATH | --config-dir PATH] [--dry-run | --apply]")
	fmt.Println("Set a configuration value with either preview or apply mode.")
}

func runConfigCheck(args []string) int {
	var configPath, configDir string
	var strict, strictAlias, jsonOut bool
	var format string

	fs := flag.NewFlagSet("check", flag.ExitOnError)
	fs.StringVar(&configPath, "config", "", "Path to configuration")
	fs.StringVar(&configDir, "config-dir", "", "Path to configuration directory")
	fs.BoolVar(&strict, "fail-on-warnings", false, "Treat warnings as errors")
	// --strict is the deprecated alias for --fail-on-warnings, kept for back-compat.
	fs.BoolVar(&strictAlias, "strict", false, "Deprecated alias for --fail-on-warnings")
	fs.StringVar(&format, "format", "human", "Output format (human, json)")
	// Handle -json alias for format=json
	fs.BoolVar(&jsonOut, "json", false, "Output in JSON")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	if strictAlias {
		fmt.Fprintln(os.Stderr, "Note: --strict is deprecated; use --fail-on-warnings")
		strict = true
	}

	if jsonOut {
		format = "json"
	}

	if configPath != "" && configDir != "" {
		fmt.Fprintln(os.Stderr, "Error: use only one of --config or --config-dir")
		return 1
	}
	if configDir != "" {
		configPath = configDir
	}
	if configPath == "" {
		discovered, err := config.DiscoverConfigDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to discover config: %v\n", err)
			return 1
		}
		configPath = discovered
	}

	// LoadWithVault: reuse the owner the load already decrypted so this verdict
	// matches the daemon's `validate_config_on_boot` admission — `config check`
	// must accept the genesis-vault, zero-token bootstrap config the daemon boots
	// into the management posture (#129, #133), as DEPLOYMENT.md §11 runs it.
	//
	// #174: doctor.Validate() covers validate_config_on_boot and nothing else. The
	// integrity half — verify_integrity_on_boot — is added below, because claiming
	// parity while never opening .checksums is what let #167 pass a pre-flight
	// gate on a box that would not boot.
	cfg, owner, err := config.LoadWithVault(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config load error: %v\n", err)
		return 1
	}

	registry, err := discoverRegistry(cfg, configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Plugin discovery error: %v\n", err)
		return 1
	}

	doc := doctor.New(cfg, registry).WithVaultPresent(owner != nil)

	// Feed doctor a stopwatch snapshot when the state DB is reachable.
	// Failure to open is non-fatal -- the check is advisory and missing
	// snapshot means doctor silently skips warnStopwatchRetention.
	if snap, snapErr := loadStopwatchSnapshot(cfg.State.Path); snapErr == nil {
		doc = doc.AddStopwatchSnapshot(snap)
	}

	result := doc.Validate()
	appendIntegrityFindings(result, cfg, configPath)

	switch format {
	case "json":
		out, err := doctor.FormatJSON(result)
		if err != nil {
			fmt.Fprintf(os.Stderr, "JSON format error: %v\n", err)
			return 1
		}
		fmt.Println(out)
	default:
		fmt.Print(doctor.FormatHuman(result))
	}

	if !result.Valid {
		return 1
	}
	if strict && len(result.Warnings) > 0 {
		return 2
	}
	return 0
}

func runConfigHashUpdate(args []string) int {
	var configPath, configDir string
	var verbose, verboseShort, dryRun bool

	fs := flag.NewFlagSet("lock", flag.ExitOnError)
	fs.StringVar(&configPath, "config", "", "Path to configuration")
	fs.StringVar(&configDir, "config-dir", "", "Path to config directory")
	fs.BoolVar(&verbose, "verbose", false, "Verbose output")
	fs.BoolVar(&verboseShort, "v", false, "Verbose output")
	fs.BoolVar(&dryRun, "dry-run", false, "Dry run")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}
	isVerbose := verbose || verboseShort

	if configPath != "" && configDir != "" {
		fmt.Fprintf(os.Stderr, "Error: use only one of --config or --config-dir\n")
		return 1
	}

	var targetDirs []string
	if configDir != "" {
		targetDirs = []string{configDir}
	} else {
		resolvedConfigPath := configPath
		if resolvedConfigPath == "" {
			discovered, err := config.DiscoverConfigDir()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to discover config: %v\n", err)
				return 1
			}
			resolvedConfigPath = discovered
		}

		dirs, err := config.DiscoverScopeDirs(resolvedConfigPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to resolve scope directories: %v\n", err)
			return 1
		}
		targetDirs = dirs
	}

	for _, dir := range targetDirs {
		configPath := filepath.Join(dir, "config.yaml")
		if _, err := os.Stat(configPath); err == nil {
			files, err := config.DiscoverConfigFiles(dir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to discover config files in %s: %v\n", dir, err)
				return 1
			}

			if isVerbose {
				fmt.Printf("Processing directory (v2 manifest): %s\n", dir)
				for _, f := range files.AllFiles() {
					tier := "operational"
					if files.FileTier(f) == config.TierHighSecurity {
						tier = "high-security"
					}
					fmt.Printf("  DISCOVER [%s] %s\n", tier, f)
				}
			}

			cfg, err := config.LoadForLock(configPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to load config for plugin locking in %s: %v\n", dir, err)
				return 1
			}
			// §3.1 (ADR Plugin Attestation): a routine `config lock` re-hashes the
			// config files only and PRESERVES the recorded plugin_fingerprints for
			// still-configured plugins (pruning de-configured ones). It never re-
			// hashes plugin bytes — that closes Threat A (lock-laundering): a lock
			// done for an unrelated reason can no longer bless a swapped binary.
			// Attestation is the explicit, per-plugin act of `ductile plugin lock`.
			// This path needs no vault nonce (nothing keyed is computed here).
			configured := make(map[string]bool, len(cfg.Plugins))
			for name, pc := range cfg.Plugins {
				configured[name] = pc.Enabled
			}
			var preserved []config.PluginFingerprint
			if existing, lerr := config.LoadChecksums(dir); lerr == nil {
				preserved = config.PreservePluginFingerprints(existing.PluginFingerprints, configured)
				if isVerbose {
					for _, fp := range preserved {
						fmt.Printf("  PRESERVE [plugin] %s manifest=%s entrypoint=%s\n",
							fp.Name, fp.ManifestPath, fp.EntrypointPath)
					}
					for _, fp := range existing.PluginFingerprints {
						if _, ok := configured[fp.Name]; !ok {
							fmt.Printf("  PRUNE [plugin] %s (no longer configured)\n", fp.Name)
						}
					}
				}
			}
			if err := config.GenerateChecksumsWithFingerprints(files, preserved, dryRun); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to lock config in %s: %v\n", dir, err)
				return 1
			}

			if isVerbose {
				if dryRun {
					fmt.Printf("  DRY-RUN .checksums: %s (not written)\n", filepath.Join(dir, ".checksums"))
				} else {
					fmt.Printf("  WROTE .checksums: %s\n", filepath.Join(dir, ".checksums"))
				}
			}
			continue
		} else if err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Failed to access %s: %v\n", configPath, err)
			return 1
		}

		scopeFiles := []string{"webhooks.yaml"}
		report, err := config.GenerateChecksumsWithReport(dir, scopeFiles, dryRun)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to lock config in %s: %v\n", dir, err)
			return 1
		}
		if isVerbose {
			fmt.Printf("Processing directory: %s\n", dir)
			for _, file := range report.Files {
				if file.Exists {
					fmt.Printf("  HASH %s: %s\n", file.Filename, file.Hash)
					continue
				}
				fmt.Printf("  SKIP %s: not found (optional)\n", file.Filename)
			}
			if dryRun {
				fmt.Printf("  DRY-RUN .checksums: %s (not written)\n", report.ChecksumPath)
			} else {
				fmt.Printf("  WROTE .checksums: %s\n", report.ChecksumPath)
			}
		}
	}

	if dryRun {
		fmt.Printf("Dry run completed for %d directory/ies (no files written):\n", len(targetDirs))
	} else {
		fmt.Printf("Successfully locked configuration in %d directory/ies:\n", len(targetDirs))
	}
	for _, dir := range targetDirs {
		fmt.Printf("  - %s\n", dir)
	}

	return 0
}

// appendIntegrityFindings gives `config check` the half of boot admission it was
// missing (#174).
//
// Before this, runConfigCheck ran doctor.Validate() and never opened .checksums,
// while its own comment claimed the verdict matched the daemon's boot admission.
// It matched validate_config_on_boot; verify_integrity_on_boot — the half that
// actually fails — was never exercised. VerifyIntegrity had exactly one non-test
// caller, reachable only from boot and reload, so the only way to test the
// manifest was to restart the daemon. With verify_integrity_on_boot that restart
// IS the outage, and docs/runbooks/privsep-thinkpad-enforce.md uses
// `config check ... # MUST be clean` as its pre-flight gate.
//
// Not breaking existing installs is the governing constraint here, so this
// follows the config's OWN admission policy rather than always checking:
//
//   - verify_integrity_on_boot: false → skipped entirely, reported as skipped.
//     The daemon does not check, so neither do we; those installs see no change.
//   - fail_on_drift is applied exactly as verifyReloadIntegrity applies it, so
//     operational drift is fatal here only where it is fatal at boot.
//
// The consequence is that this can only newly fail a config that would also fail
// to boot — which is the entire point, and the opposite of a regression.
func appendIntegrityFindings(result *doctor.Result, cfg *config.Config, configPath string) {
	if result == nil || cfg == nil {
		return
	}
	admission := cfg.Service.AdmissionPolicy()
	if !admission.VerifyIntegrityOnBoot {
		result.Warnings = append(result.Warnings, doctor.Issue{
			Category: "integrity",
			Field:    "service.admission.verify_integrity_on_boot",
			Message:  "integrity not checked: verify_integrity_on_boot is false, so the daemon does not verify .checksums at boot either",
		})
		return
	}

	configDir := resolveConfigDir(configPath)
	files, err := config.DiscoverConfigFiles(configDir)
	if err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, doctor.Issue{
			Category: "integrity",
			Message: fmt.Sprintf("cannot read config directory %s: %v%s",
				configDir, err, fsown.Hint(configDir)),
		})
		return
	}

	integrity, err := config.VerifyIntegrity(configDir, files)
	if err != nil || integrity == nil {
		result.Valid = false
		result.Errors = append(result.Errors, doctor.Issue{
			Category: "integrity",
			Message:  fmt.Sprintf("integrity check failed to run: %v", err),
		})
		return
	}

	for _, msg := range integrity.Errors {
		result.Valid = false
		result.Errors = append(result.Errors, doctor.Issue{Category: "integrity", Message: msg})
	}
	for _, msg := range integrity.Warnings {
		// Mirror verifyReloadIntegrity: drift is a warning unless the running
		// admission policy promotes it, in which case the daemon rejects and so
		// must this verdict.
		if admission.FailOnDrift {
			result.Valid = false
			result.Errors = append(result.Errors, doctor.Issue{
				Category: "integrity",
				Message:  fmt.Sprintf("%s (fatal: admission.fail_on_drift)", msg),
			})
			continue
		}
		result.Warnings = append(result.Warnings, doctor.Issue{Category: "integrity", Message: msg})
	}
}
