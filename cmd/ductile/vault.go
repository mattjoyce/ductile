package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mattjoyce/ductile/internal/secrets"
	"github.com/mattjoyce/ductile/internal/vault"
)

func runVaultNoun(args []string) int {
	if len(args) < 1 {
		printVaultNounHelp(os.Stderr)
		return 1
	}
	if isHelpToken(args[0]) {
		printVaultNounHelp(os.Stdout)
		return 0
	}

	action := args[0]
	actionArgs := args[1:]

	switch action {
	case "init":
		return runVaultInit(actionArgs)
	case "help":
		printVaultNounHelp(os.Stdout)
		return 0
	default:
		// #nosec G705 -- stderr output is plain text, not HTML.
		fmt.Fprintf(os.Stderr, "Unknown vault action: %s\n", action)
		printVaultNounHelp(os.Stderr)
		return 1
	}
}

func printVaultNounHelp(w *os.File) {
	_, _ = fmt.Fprintln(w, "Usage: ductile vault <action>")
	_, _ = fmt.Fprintln(w, "Actions:")
	_, _ = fmt.Fprintln(w, "  init   Create a brand-new vault (genesis): seeds core + nonce + admin token")
}

// runVaultInit bootstraps a new vault. It is a local, key-touching operation —
// genesis happens before any daemon/API exists, so it never goes over the API.
func runVaultInit(args []string) int {
	fs := flag.NewFlagSet("vault init", flag.ContinueOnError)
	vaultPath := fs.String("vault", "", "Path to write the new vault blob")
	keyPath := fs.String("key", "", "Path to the age identity (private key) that encrypts the vault")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *vaultPath == "" || *keyPath == "" {
		fmt.Fprintln(os.Stderr, "vault init: --vault and --key are required")
		return 1
	}

	kr, err := secrets.LoadKeyringFromFile(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault init: %v\n", err)
		return 1
	}

	_, adminToken, err := vault.Init(*vaultPath, kr, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault init: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "Wrote vault to %s (encrypted; core principal + fingerprint nonce seeded).\n", *vaultPath)
	fmt.Fprintln(os.Stderr, "Initial admin token (shown once — store it now, it is not recoverable in plaintext):")
	// The token itself goes to stdout so it can be captured/piped cleanly.
	fmt.Println(adminToken)
	return 0
}
