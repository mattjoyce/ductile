package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/doctor"
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
	case "token":
		return runConfigToken(actionArgs)
	case "scope":
		return runConfigScope(actionArgs)
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

	fs := flag.NewFlagSet("show", flag.ExitOnError)
	fs.StringVar(&configPath, "config", "", "Path to configuration file or directory")
	fs.StringVar(&configDir, "config-dir", "", "Path to configuration directory")
	fs.BoolVar(&jsonOut, "json", false, "Output in structured JSON format")

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

	fs := flag.NewFlagSet("get", flag.ExitOnError)
	fs.StringVar(&configPath, "config", "", "Path to configuration file or directory")
	fs.StringVar(&configDir, "config-dir", "", "Path to configuration directory")
	fs.BoolVar(&jsonOut, "json", false, "Output in structured JSON format")

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
		fmt.Fprintf(os.Stderr, "Usage: ductile config get <path> [--json]\n")
		return 1
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
	if configPath != "" && configDir != "" {
		return nil, fmt.Errorf("use only one of --config or --config-dir")
	}
	if configDir != "" {
		configPath = configDir
	}
	if configPath == "" {
		discovered, err := config.DiscoverConfigDir()
		if err != nil {
			return nil, err
		}
		configPath = discovered
	}
	return config.Load(configPath)
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
	_, _ = fmt.Fprintln(w, "Actions: lock, check, show, get, set, token, scope, plugin, route, webhook, init, backup, restore")
}

func printConfigLockHelp() {
	fmt.Println("Usage: ductile config lock [--config PATH | --config-dir PATH] [-v|--verbose] [--dry-run]")
	fmt.Println("Authorize current configuration state by regenerating scope file integrity hashes.")
}

func printConfigCheckHelp() {
	fmt.Println("Usage: ductile config check [--config PATH | --config-dir PATH] [--format human|json] [--strict] [--json]")
	fmt.Println("Validate configuration syntax, policy, and integrity.")
}

func printConfigShowHelp() {
	fmt.Println("Usage: ductile config show [entity] [--config PATH | --config-dir PATH] [--json]")
	fmt.Println("Show full resolved configuration or a filtered entity node.")
}

func printConfigGetHelp() {
	fmt.Println("Usage: ductile config get <path> [--config PATH | --config-dir PATH] [--json]")
	fmt.Println("Read a single value from the resolved configuration.")
}

func printConfigSetHelp() {
	fmt.Println("Usage: ductile config set <path>=<value> [--config PATH | --config-dir PATH] [--dry-run | --apply]")
	fmt.Println("Set a configuration value with either preview or apply mode.")
}

func runConfigCheck(args []string) int {
	var configPath, configDir string
	var strict, jsonOut bool
	var format string

	fs := flag.NewFlagSet("check", flag.ExitOnError)
	fs.StringVar(&configPath, "config", "", "Path to configuration")
	fs.StringVar(&configDir, "config-dir", "", "Path to configuration directory")
	fs.BoolVar(&strict, "strict", false, "Treat warnings as errors")
	fs.StringVar(&format, "format", "human", "Output format (human, json)")
	// Handle -json alias for format=json
	fs.BoolVar(&jsonOut, "json", false, "Output in JSON")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
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

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config load error: %v\n", err)
		return 1
	}

	registry, err := discoverRegistry(cfg, configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Plugin discovery error: %v\n", err)
		return 1
	}

	doc := doctor.New(cfg, registry)

	// Feed doctor a stopwatch snapshot when the state DB is reachable.
	// Failure to open is non-fatal -- the check is advisory and missing
	// snapshot means doctor silently skips warnStopwatchRetention.
	if snap, snapErr := loadStopwatchSnapshot(cfg.State.Path); snapErr == nil {
		doc = doc.AddStopwatchSnapshot(snap)
	}

	result := doc.Validate()

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
			resolved, err := resolveConfiguredPluginFingerprints(cfg, configPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to resolve plugin fingerprints in %s: %v\n", dir, err)
				return 1
			}
			if isVerbose {
				for _, rp := range resolved {
					fmt.Printf("  DISCOVER [plugin] %s manifest=%s entrypoint=%s enabled=%t\n",
						rp.Name, rp.ManifestPath, rp.EntrypointPath, rp.Enabled)
				}
			}
			if err := config.GenerateChecksumsWithPlugins(files, resolved, dryRun); err != nil {
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

		scopeFiles := []string{"tokens.yaml", "webhooks.yaml"}
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
