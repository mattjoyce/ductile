package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

// appendClearpassAudit writes one clearpass audit row from the CLI, opening the
// state database the gateway uses (WAL, so a running gateway is fine). The
// gateway's closer refuses to commit a window the log never saw opened, so a
// failed open row fails the open.
func appendClearpassAudit(cfg *config.Config, ev state.VaultAuditEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := storage.OpenSQLite(ctx, cfg.State.Path)
	if err != nil {
		return fmt.Errorf("open state database: %w", err)
	}
	defer func() { _ = db.Close() }()
	return state.NewStore(db).AppendVaultAudit(ctx, ev)
}

// runPluginClearpass implements `ductile plugin clearpass` (card #144): the
// operator's bounded pre-authorisation to change one plugin's bytes.
//
//	ductile plugin clearpass <name> --for 2h [--note "card 208"]   open a window
//	ductile plugin clearpass <name> --close                          commit now
//	ductile plugin clearpass --list                                   show windows
//
// Opening and closing both need the vault, exactly like `plugin lock`: a window
// is sealed under the fingerprint nonce so that a .checksums write without the
// vault cannot open one, and closing hashes the live bytes with that nonce.
func runPluginClearpass(args []string) int {
	var configPath, configDir, note string
	var window time.Duration
	var closeFlag, listFlag bool

	fs := flag.NewFlagSet("clearpass", flag.ContinueOnError)
	fs.StringVar(&configPath, "config", "", "Path to configuration")
	fs.StringVar(&configDir, "config-dir", "", "Path to config directory")
	fs.DurationVar(&window, "for", 0, "How long to accept changed bytes (required to open; at most 24h)")
	fs.StringVar(&note, "note", "", "Why the window is open (recorded with it)")
	fs.BoolVar(&closeFlag, "close", false, "Close the window now: commit the live bytes as the attestation")
	fs.BoolVar(&listFlag, "list", false, "Show every recorded clearpass window")
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

	switch {
	case listFlag:
		if len(rest) != 0 || closeFlag || window != 0 {
			printPluginClearpassHelp()
			return 1
		}
		return runPluginClearpassList(dir)
	case len(rest) != 1:
		printPluginClearpassHelp()
		return 1
	case closeFlag:
		if window != 0 {
			fmt.Fprintln(os.Stderr, "Error: --close takes no --for")
			return 1
		}
		return runPluginClearpassClose(dir, configPath, cfg, rest[0])
	case window == 0:
		fmt.Fprintln(os.Stderr, "Error: --for <duration> is required to open a clearpass (for example --for 2h)")
		return 1
	default:
		return runPluginClearpassOpen(dir, cfg, rest[0], window, note)
	}
}

func runPluginClearpassOpen(dir string, cfg *config.Config, name string, window time.Duration, note string) int {
	if _, ok := cfg.Plugins[name]; !ok {
		fmt.Fprintf(os.Stderr, "plugin %q is not configured\n", name)
		return 1
	}
	cp, err := config.NewClearpass(time.Now(), window, note)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clearpass: %v\n", err)
		return 1
	}
	manifest, err := config.LoadChecksums(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clearpass: %v; run 'ductile config lock' first\n", err)
		return 1
	}
	recorded, ok := manifest.FindPluginFingerprint(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "plugin %q has no recorded fingerprint; a clearpass relaxes an attestation, so attest it first with 'ductile plugin lock %s'\n", name, name)
		return 1
	}
	nonce, err := fingerprintNonceForConfig(dir, cfg, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clearpass: %v\n", err)
		return 1
	}
	cp, err = cp.Sealed(recorded, nonce)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clearpass: %v\n", err)
		return 1
	}
	updated, _ := config.SetPluginClearpass(manifest.PluginFingerprints, name, &cp)
	// The open row goes first: the gateway commits only a window the log saw
	// opened, so a record without its row is inert rather than dangerous.
	if err := appendClearpassAudit(cfg, state.VaultAuditEvent{
		Op: auditOpClearpassOpen, Principal: name, Actor: "cli", Outcome: "ok", Detail: clearpassOpenDetail(cp),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "clearpass: cannot record the window in the audit log, so it was not opened: %v\n", err)
		return 1
	}
	if err := config.WritePluginFingerprints(dir, updated, false); err != nil {
		fmt.Fprintf(os.Stderr, "clearpass: failed to write .checksums: %v\n", err)
		return 1
	}
	fmt.Printf("Clearpass open for plugin %q until %s (%s).\n", name, cp.Until, window)
	fmt.Println("Changed bytes will be accepted at spawn and committed as the attestation when the window closes.")
	fmt.Printf("Close it early with 'ductile plugin clearpass %s --close' or 'ductile plugin lock %s'.\n", name, name)
	return 0
}

func runPluginClearpassClose(dir, configPath string, cfg *config.Config, name string) int {
	manifest, err := config.LoadChecksums(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clearpass: %v\n", err)
		return 1
	}
	recorded, ok := manifest.FindPluginFingerprint(name)
	if !ok || recorded.Clearpass == nil {
		fmt.Fprintf(os.Stderr, "plugin %q has no clearpass to close\n", name)
		return 1
	}
	resolved, err := resolveConfiguredPluginFingerprints(cfg, configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clearpass: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "plugin %q is not configured or was not discovered on disk\n", name)
		return 1
	}
	nonce, err := fingerprintNonceForConfig(dir, cfg, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clearpass: %v\n", err)
		return 1
	}
	commit, err := commitClearpass(dir, recorded, *target, nonce)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	// The CLI does not count spawns; the gateway's own close does. -1 prints as
	// "observed spawns unknown". The close row is what stops the record from
	// being committed again if it ever reappears.
	detail := clearpassCloseDetail(commit, -1)
	if err := appendClearpassAudit(cfg, state.VaultAuditEvent{
		Op: auditOpClearpassClose, Principal: name, Actor: "cli", Outcome: "ok", Detail: detail,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: the window is committed but its close could not be recorded in the audit log: %v\n", err)
	}
	fmt.Printf("Clearpass closed: %s.\n", detail)
	return 0
}

func runPluginClearpassList(dir string) int {
	manifest, err := config.LoadChecksums(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clearpass: %v\n", err)
		return 1
	}
	now := time.Now()
	shown := 0
	for _, fp := range manifest.PluginFingerprints {
		if fp.Clearpass == nil {
			continue
		}
		if shown == 0 {
			fmt.Printf("%-24s  %-9s  %-20s  %-20s  %s\n", "PLUGIN", "STATE", "OPENED", "UNTIL", "NOTE")
		}
		status := "open"
		if fp.Clearpass.Expired(now) {
			status = "expired"
		} else if !fp.Clearpass.Open(now) {
			status = "malformed"
		}
		fmt.Printf("%-24s  %-9s  %-20s  %-20s  %s\n", fp.Name, status, fp.Clearpass.OpenedAt, fp.Clearpass.Until, fp.Clearpass.Note)
		shown++
	}
	if shown == 0 {
		fmt.Println("No clearpass windows recorded.")
	}
	return 0
}

func printPluginClearpassHelp() {
	fmt.Println("Usage: ductile plugin clearpass <name> --for <duration> [--note TEXT] [--config-dir DIR]")
	fmt.Println("       ductile plugin clearpass <name> --close [--config-dir DIR]")
	fmt.Println("       ductile plugin clearpass --list [--config-dir DIR]")
	fmt.Println("Accept changed bytes for one attested plugin until a deadline (at most 24h).")
	fmt.Println("Inside the window a fingerprint mismatch at spawn is accepted and reported;")
	fmt.Println("at the deadline the gateway commits the live bytes as the attestation and")
	fmt.Println("says what it accepted. --close commits now. 'plugin lock' also clears a window.")
}
