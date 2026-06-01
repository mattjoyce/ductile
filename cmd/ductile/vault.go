package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
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
	case "import":
		return runVaultImport(actionArgs)
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
	_, _ = fmt.Fprintln(w, "  init     Create a brand-new vault (genesis): seeds core + nonce + admin token")
	_, _ = fmt.Fprintln(w, "  import   Migrate tokens.yaml entries into an existing vault")
}

// runVaultImport migrates a legacy tokens.yaml table into an existing vault. It
// is a local, key-touching operation (it reads the key and rewrites the blob),
// never over the API — like init. Literal values move in; ${ENV} pointers are
// flagged unless --resolve-env is given, in which case resolvable ones import
// their resolved value and the rest are flagged for manual re-provisioning.
func runVaultImport(args []string) int {
	fs := flag.NewFlagSet("vault import", flag.ContinueOnError)
	vaultPath := fs.String("vault", "", "Path to the vault blob to import into")
	keyPath := fs.String("key", "", "Path to the age identity (private key)")
	tokensPath := fs.String("tokens", "", "Path to the tokens.yaml to import from")
	resolveEnv := fs.Bool("resolve-env", false, "Import the resolved value of ${ENV} entries instead of flagging them")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *vaultPath == "" || *keyPath == "" || *tokensPath == "" {
		fmt.Fprintln(os.Stderr, "vault import: --vault, --key, and --tokens are required")
		return 1
	}

	kr, err := secrets.LoadKeyringFromFile(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault import: %v\n", err)
		return 1
	}
	v, err := vault.Load(*vaultPath, kr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault import: %v\n  (run 'ductile vault init' first)\n", err)
		return 1
	}

	entries, err := config.ReadRawTokens(*tokensPath, kr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault import: %v\n", err)
		return 1
	}

	plan := config.PlanTokenImport(entries, *resolveEnv, os.LookupEnv)

	now := time.Now()
	imported := 0
	for _, s := range plan.Imported {
		if err := v.Store().SetSecret(s.Name, s.Value, nil, vault.PatternManual, now); err != nil {
			plan.Flagged = append(plan.Flagged, config.FlaggedSecret{Name: s.Name, Reason: "could not register: " + err.Error()})
			continue
		}
		imported++
	}
	if err := v.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "vault import: save: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "Imported %d secret(s) into the vault.\n", imported)
	if len(plan.Flagged) > 0 {
		fmt.Fprintf(os.Stderr, "Flagged %d entr(ies) needing attention:\n", len(plan.Flagged))
		for _, f := range plan.Flagged {
			fmt.Fprintf(os.Stderr, "  - %s: %s\n", f.Name, f.Reason)
		}
	}
	return 0
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
