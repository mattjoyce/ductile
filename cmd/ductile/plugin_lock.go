package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mattjoyce/ductile/internal/config"
)

// runPluginLock implements `ductile plugin lock` — the explicit, per-plugin
// attestation act (ADR §3.1). `config lock` no longer re-blesses plugin bytes;
// this verb is the only producer of plugin_fingerprints.
//
//	ductile plugin lock <name>        attest exactly one named plugin (keyed)
//	ductile plugin lock --all         preview every changed/new plugin + a code
//	ductile plugin lock --all <code>  commit, but only if <code> still matches
func runPluginLock(args []string) int {
	var configPath, configDir string
	var allFlag, dryRun bool

	fs := flag.NewFlagSet("lock", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "", "Path to configuration")
	fs.StringVar(&configDir, "config-dir", "", "Path to config directory")
	fs.BoolVar(&allFlag, "all", false, "Attest all configured plugins (preview, then commit with the printed code)")
	fs.BoolVar(&dryRun, "dry-run", false, "Preview the write without modifying .checksums")
	// parseFlagsAndPositionals (config_manage.go) lets flags appear before OR after
	// positionals; the stdlib flag package stops at the first non-flag, which would
	// silently drop a trailing --config-dir. ductile is LLM-operated, so flags land
	// in any order.
	rest, err := parseFlagsAndPositionals(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
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
	dir := resolveConfigDir(configPath)

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config load error: %v\n", err)
		return 1
	}

	if allFlag {
		return runPluginLockAll(dir, configPath, cfg, rest, dryRun)
	}
	if len(rest) != 1 {
		printPluginLockHelp()
		return 1
	}
	return runPluginLockOne(dir, configPath, cfg, rest[0], dryRun)
}

// runPluginLockOne re-hashes exactly the named plugin and merges its single entry
// into the recorded set, leaving every other plugin's fingerprint untouched.
func runPluginLockOne(dir, configPath string, cfg *config.Config, name string, dryRun bool) int {
	if _, ok := cfg.Plugins[name]; !ok {
		fmt.Fprintf(os.Stderr, "plugin %q is not configured; add it to config.yaml before attesting it\n", name)
		return 1
	}
	resolved, err := resolveConfiguredPluginFingerprints(cfg, configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve plugin %q: %v\n", name, err)
		return 1
	}
	var target *config.ResolvedPlugin
	for i := range resolved {
		if resolved[i].Name == name {
			target = &resolved[i]
			break
		}
	}
	if target == nil {
		fmt.Fprintf(os.Stderr, "plugin %q is configured but was not discovered on disk; restore its files before attesting\n", name)
		return 1
	}

	nonce, err := fingerprintNonceForConfig(dir, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plugin lock: %v\n", err)
		return 1
	}
	fpNew, err := config.ComputePluginFingerprint(*target, nonce)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plugin lock: %v\n", err)
		return 1
	}

	existing, err := config.LoadChecksums(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plugin lock: %v; run 'ductile config lock' first to lock the config files\n", err)
		return 1
	}
	merged := config.MergePluginFingerprint(existing.PluginFingerprints, fpNew)
	if err := config.WritePluginFingerprints(dir, merged, dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "plugin lock: failed to write .checksums: %v\n", err)
		return 1
	}
	if dryRun {
		fmt.Printf("DRY-RUN: would attest plugin %q (entrypoint %s)\n", name, shortFP(fpNew.EntrypointHash))
		return 0
	}
	fmt.Printf("Attested plugin %q (entrypoint %s, manifest %s).\n", name, shortFP(fpNew.EntrypointHash), shortFP(fpNew.ManifestHash))
	return 0
}

// runPluginLockAll previews (no code) or commits (matching code) attestation of
// the full configured plugin set. The proposed set is rebuilt from currently-
// configured plugins, so de-configured entries are pruned. The confirmation code
// is bound to the proposed bytes, so it self-invalidates if any plugin changes
// between preview and commit.
func runPluginLockAll(dir, configPath string, cfg *config.Config, rest []string, dryRun bool) int {
	if len(rest) > 1 {
		printPluginLockHelp()
		return 1
	}

	resolved, err := resolveConfiguredPluginFingerprints(cfg, configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve configured plugins: %v\n", err)
		return 1
	}

	var nonce []byte
	if len(resolved) > 0 {
		nonce, err = fingerprintNonceForConfig(dir, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plugin lock --all: %v\n", err)
			return 1
		}
	}

	newSet := make([]config.PluginFingerprint, 0, len(resolved))
	for _, rp := range resolved {
		fp, ferr := config.ComputePluginFingerprint(rp, nonce)
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "plugin lock --all: %v\n", ferr)
			return 1
		}
		newSet = append(newSet, fp)
	}

	existing, err := config.LoadChecksums(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plugin lock --all: %v; run 'ductile config lock' first to lock the config files\n", err)
		return 1
	}

	changed, added, removed := config.DiffPluginFingerprints(existing.PluginFingerprints, newSet)
	if len(changed)+len(added)+len(removed) == 0 {
		fmt.Println("Nothing to attest: every configured plugin already matches its recorded fingerprint.")
		return 0
	}

	code := config.PluginFingerprintsCode(newSet)

	if len(rest) == 0 {
		printPluginLockAllPreview(changed, added, removed, code)
		return 0
	}

	supplied := rest[0]
	if supplied != code {
		fmt.Fprintf(os.Stderr,
			"confirmation code %q does not match the current change set (expected %q): the plugins on disk changed since you previewed — re-run 'ductile plugin lock --all' and confirm the new code\n",
			supplied, code)
		return 1
	}

	if err := config.WritePluginFingerprints(dir, newSet, dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "plugin lock --all: failed to write .checksums: %v\n", err)
		return 1
	}
	if dryRun {
		fmt.Printf("DRY-RUN: would attest %d plugin(s) (changed=%d added=%d removed=%d).\n",
			len(newSet), len(changed), len(added), len(removed))
		return 0
	}
	fmt.Printf("Attested %d plugin(s): changed=%d added=%d removed=%d.\n",
		len(newSet), len(changed), len(added), len(removed))
	return 0
}

func printPluginLockAllPreview(changed, added, removed []string, code string) {
	fmt.Println("Plugin attestation preview — the following fingerprints would change:")
	for _, n := range added {
		fmt.Printf("  + new      %s\n", n)
	}
	for _, n := range changed {
		fmt.Printf("  ~ changed  %s\n", n)
	}
	for _, n := range removed {
		fmt.Printf("  - removed  %s (no longer configured)\n", n)
	}
	fmt.Printf("\nReview the changes above, then commit with:\n  ductile plugin lock --all %s\n", code)
}

func shortFP(h string) string {
	if len(h) < 12 {
		return h
	}
	return h[:12]
}

func printPluginLockHelp() {
	fmt.Println("Usage:")
	fmt.Println("  ductile plugin lock <name>        Attest one configured plugin (keyed, explicit).")
	fmt.Println("  ductile plugin lock --all         Preview every changed/new plugin and print a confirm code.")
	fmt.Println("  ductile plugin lock --all <code>  Commit the previewed attestation (code must still match).")
	fmt.Println("Flags: [--config PATH | --config-dir PATH] [--dry-run]")
	fmt.Println("Attestation is keyed with the vault nonce and requires a loadable vault.")
}
