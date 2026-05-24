package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/plugin"
)

var (
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

//go:embed templates/skills-core-mode.md
var skillsCoreMode string

//go:embed templates/skills-cli-commands.md
var skillsCLICommands string

func main() {
	os.Exit(runCLI(os.Args[1:]))
}

func runCLI(cliArgs []string) int {
	if len(cliArgs) < 1 {
		printUsage()
		return 1
	}

	cmd := cliArgs[0]
	args := cliArgs[1:]

	if cmd == "--version" {
		return runVersion(args)
	}

	switch cmd {
	// --- NOUNS ---
	case "system":
		return runSystemNoun(args)
	case "config":
		return runConfigNoun(args)
	case "job":
		return runJobNoun(args)
	case "plugin":
		return runPluginNoun(args)
	case "relay":
		return runRelayNoun(args)
	case "skills":
		return runSystemSkills(args)
	case "api":
		return runAPINoun(args)
	case "stopwatch":
		return runStopwatchNoun(args)

	case "version":
		return runVersion(args)
	case "help", "--help", "-h":
		printUsage()
		return 0

	default:
		// #nosec G705 -- stderr output is plain text, not HTML.
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		printUsage()
		return 1
	}
}

type versionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
}

func runVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "Output version metadata as JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "Usage: ductile version [--json]")
		return 1
	}

	info := currentVersionInfo()

	if *jsonOut {
		data, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to render version JSON: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	fmt.Printf("ductile %s\n", info.Version)
	fmt.Printf("commit: %s\n", info.Commit)
	fmt.Printf("built_at: %s\n", info.BuildTime)
	return 0
}

func currentVersionInfo() versionInfo {
	info := versionInfo{
		Version:   strings.TrimSpace(version),
		Commit:    "unknown",
		BuildTime: "unknown",
	}

	if info.Version == "" {
		info.Version = "0.0.0-dev"
	}

	resolvedCommit := strings.TrimSpace(gitCommit)
	if resolvedCommit == "" || resolvedCommit == "unknown" {
		resolvedCommit = strings.TrimSpace(readBuildSetting("vcs.revision"))
	}
	if resolvedCommit != "" {
		info.Commit = shortenCommit(resolvedCommit)
	}

	resolvedBuildTime := strings.TrimSpace(buildDate)
	if resolvedBuildTime == "" || resolvedBuildTime == "unknown" {
		resolvedBuildTime = strings.TrimSpace(readBuildSetting("vcs.time"))
	}
	if normalizedBuildTime, ok := normalizeBuildTimeUTC(resolvedBuildTime); ok {
		info.BuildTime = normalizedBuildTime
	}

	return info
}

func shortenCommit(commit string) string {
	if len(commit) <= 12 {
		return commit
	}
	return commit[:12]
}

func normalizeBuildTimeUTC(raw string) (string, bool) {
	if raw == "" || raw == "unknown" {
		return "", false
	}

	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "", false
	}

	return t.UTC().Format(time.RFC3339), true
}

func readBuildSetting(key string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == key {
			return setting.Value
		}
	}
	return ""
}

func validateScheduledCommands(cfg *config.Config, registry *plugin.Registry) error {
	if cfg == nil || registry == nil {
		return nil
	}

	for pluginName, pluginConf := range cfg.Plugins {
		if !pluginConf.Enabled {
			continue
		}

		plug, ok := registry.Get(pluginName)
		if !ok {
			return fmt.Errorf("plugin %q is configured but not discoverable", pluginName)
		}

		for _, schedule := range pluginConf.NormalizedSchedules() {
			command := strings.TrimSpace(schedule.Command)
			if command == "" {
				command = "poll"
			}

			scheduleID := strings.TrimSpace(schedule.ID)
			if scheduleID == "" {
				scheduleID = "default"
			}

			if !plug.SupportsCommand(command) {
				return fmt.Errorf("plugin %q schedule %q references unsupported command %q", pluginName, scheduleID, command)
			}
		}
	}

	return nil
}

func printUsage() {
	fmt.Print(`ductile - Lightweight, open-source integration engine for the agentic era.

Usage:
  ductile <noun> <action> [flags]

Core Resources (Nouns):
  system    Gateway lifecycle and health
  config    System configuration and integrity
  job       Execution instances and lineage
  plugin    Capability discovery and management (Connectors)
  stopwatch Per-invocation timing ledger (prune retention)
  api       Directly call the gateway API

System Commands:
  system start      Start the integration gateway in foreground
  system status     Show global gateway health
  system plugin-facts Show recent append-only plugin facts
  system scheduler  Show scheduler-submitted polls currently in flight
  system reset      Reset a plugin/connector circuit breaker
  system reload     Reload configuration in a running gateway
  system skills     Export capability registry (Skills) as LLM-readable Markdown

Config Commands:
  config lock       Authorize current state (update integrity hashes)
  config check      Validate syntax, policy, and integrity
  config show       Show full resolved configuration or a filtered entity node
  config get        Read a single value from the resolved configuration
  config set        Set a configuration value with either preview or apply mode
  config token      Manage scoped tokens
  config scope      Manage token scopes
  config plugin     Manage plugin configuration
  config route      Manage event routes
  config webhook    Manage webhooks
  config init       Initialize config directory
  config backup     Create backup archive
  config restore    Restore config backup

Job Commands:
  job inspect <id>  Show lineage, baggage, and execution details
  job logs          Query stored job logs for audit and troubleshooting

Plugin Commands:
  plugin list       Show discovered plugins/connectors
  plugin run <name> Manual execution

Relay Commands:
  relay send <instance> Send one authenticated remote relay event

API Commands:
  api <endpoint>    Directly call the gateway API (uses configured key)

Capability Discovery (Skills):
  skills            Export live capability registry as LLM-readable Markdown

General:
  --version         Show version information
  version           Show version information
  help              Show this help message

Use 'ductile <noun> help' for resource-specific flags.
`)
}

// --- NOUN DISPATCHERS ---

func getPIDLockPath(cfg *config.Config) string {
	dbPath := cfg.State.Path
	dbDir := filepath.Dir(dbPath)
	dbBase := filepath.Base(dbPath)
	ext := filepath.Ext(dbBase)
	nameWithoutExt := dbBase[:len(dbBase)-len(ext)]
	return filepath.Join(dbDir, nameWithoutExt+".pid")
}

func readPIDFromFile(path string) (int, error) {
	// #nosec G304 -- pid lock path is operator-controlled local input.
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("failed to read pid file %s: %w", path, err)
	}
	raw := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid pid in %s", path)
	}
	return pid, nil
}

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}
