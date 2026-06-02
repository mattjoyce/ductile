package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
	case "set":
		return runVaultSet(actionArgs)
	case "register-principal":
		return runVaultRegisterPrincipal(actionArgs)
	case "roll":
		return runVaultRoll(actionArgs)
	case "revoke":
		return runVaultNameOp("revoke", "/vault/secret/revoke", "Revoked secret", actionArgs)
	case "revoke-principal":
		return runVaultNameOp("revoke-principal", "/vault/principal/revoke", "Revoked principal", actionArgs)
	case "purge-principal":
		return runVaultNameOp("purge-principal", "/vault/principal/purge", "Purged principal", actionArgs)
	case "roll-principal":
		return runVaultRollPrincipal(actionArgs)
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
	_, _ = fmt.Fprintln(w, "  register-principal Register a deliver-to principal (--name --kind plugin|consumer|gateway)")
	_, _ = fmt.Fprintln(w, "  set              Set a secret via the daemon's management API (value from stdin)")
	_, _ = fmt.Fprintln(w, "  roll             Roll a secret's value (manual: value from stdin; auto: daemon-minted)")
	_, _ = fmt.Fprintln(w, "  revoke           Revoke a secret (terminal)")
	_, _ = fmt.Fprintln(w, "  revoke-principal Revoke a principal (its secrets stop being delivered)")
	_, _ = fmt.Fprintln(w, "  purge-principal  Remove a principal and strip all its grants")
	_, _ = fmt.Fprintln(w, "  roll-principal   Roll every auto secret a principal holds")
}

// runVaultSet sets a secret through the daemon's authenticated management API.
// It is a keyless API client: it holds no age key and decrypts nothing — the
// daemon (the sole writer) owns the key and persists the change. The secret
// VALUE is read from stdin, never argv, so it cannot leak via /proc.
func runVaultSet(args []string) int {
	fs := flag.NewFlagSet("vault set", flag.ContinueOnError)
	apiURL := fs.String("api-url", "", "Daemon API base URL (e.g. http://127.0.0.1:8080)")
	token := fs.String("token", "", "Vault admin token (or set DUCTILE_VAULT_TOKEN)")
	name := fs.String("name", "", "Secret name")
	pattern := fs.String("pattern", vault.PatternManual, "Provisioning pattern: manual|auto")
	principalsCSV := fs.String("principal", "", "Comma-separated principals authorized to receive the secret")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "vault set: --name is required")
		return 1
	}

	tok := resolveVaultToken(*token)

	// Read the value from stdin so the secret never appears on argv.
	valueBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault set: read value from stdin: %v\n", err)
		return 1
	}
	value := strings.TrimRight(string(valueBytes), "\r\n")

	var principals []string
	for _, p := range strings.Split(*principalsCSV, ",") {
		if p = strings.TrimSpace(p); p != "" {
			principals = append(principals, p)
		}
	}

	if err := doVaultSet(*apiURL, tok, *name, value, principals, *pattern); err != nil {
		fmt.Fprintf(os.Stderr, "vault set: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Set secret %q via the daemon.\n", *name)
	return 0
}

// doVaultSet POSTs a secret to the daemon's /vault/secret endpoint.
func doVaultSet(apiURL, token, name, value string, principals []string, pattern string) error {
	_, err := vaultAPIPost(apiURL, token, "/vault/secret", map[string]any{
		"name":                  name,
		"value":                 value,
		"authorized_principals": principals,
		"pattern":               pattern,
	})
	return err
}

// vaultAPIPost POSTs a JSON body to a vault management endpoint with the vault
// admin token as the Bearer credential, returning the response body on 200.
// Mutation is API-only by design (the daemon is the sole writer), so both an
// API URL and a token are required.
func vaultAPIPost(apiURL, token, path string, body any) ([]byte, error) {
	if strings.TrimSpace(apiURL) == "" {
		return nil, fmt.Errorf("--api-url is required (vault writes go through the daemon)")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("vault admin token required (--token or DUCTILE_VAULT_TOKEN)")
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(apiURL, "/") + path
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s failed (%d): %s", path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

// resolveVaultToken prefers the flag, then the DUCTILE_VAULT_TOKEN env var.
func resolveVaultToken(flagToken string) string {
	if t := strings.TrimSpace(flagToken); t != "" {
		return t
	}
	return strings.TrimSpace(os.Getenv("DUCTILE_VAULT_TOKEN"))
}

// readPipedStdin returns trimmed stdin only when it is piped (not a terminal),
// so a name-only command never blocks waiting for input.
func readPipedStdin() (string, error) {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeCharDevice != 0 {
		return "", nil // interactive terminal — no piped value
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// runVaultRegisterPrincipal admits a deliver-to identity into the vault over the
// management API, so secrets can then be granted to it (`vault set --principal`).
func runVaultRegisterPrincipal(args []string) int {
	fs := flag.NewFlagSet("vault register-principal", flag.ContinueOnError)
	apiURL := fs.String("api-url", "", "Daemon API base URL")
	token := fs.String("token", "", "Vault admin token (or DUCTILE_VAULT_TOKEN)")
	name := fs.String("name", "", "Principal name")
	kind := fs.String("kind", "plugin", "Principal kind: plugin|consumer|gateway")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "vault register-principal: --name is required")
		return 1
	}
	if _, err := vaultAPIPost(*apiURL, resolveVaultToken(*token), "/vault/principal",
		map[string]any{"name": *name, "kind": *kind}); err != nil {
		fmt.Fprintf(os.Stderr, "vault register-principal: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Registered principal %q (kind %s).\n", *name, *kind)
	return 0
}

// runVaultRoll rolls a single secret. For a manual-pattern secret the new value
// is read from stdin (never argv); for an auto-pattern secret the daemon mints
// it and any stdin is ignored.
func runVaultRoll(args []string) int {
	fs := flag.NewFlagSet("vault roll", flag.ContinueOnError)
	apiURL := fs.String("api-url", "", "Daemon API base URL")
	token := fs.String("token", "", "Vault admin token (or DUCTILE_VAULT_TOKEN)")
	name := fs.String("name", "", "Secret name")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "vault roll: --name is required")
		return 1
	}
	value, err := readPipedStdin()
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault roll: read value from stdin: %v\n", err)
		return 1
	}
	if _, err := vaultAPIPost(*apiURL, resolveVaultToken(*token), "/vault/secret/roll",
		map[string]any{"name": *name, "value": value}); err != nil {
		fmt.Fprintf(os.Stderr, "vault roll: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Rolled secret %q.\n", *name)
	return 0
}

// runVaultNameOp drives the name-only lifecycle commands (revoke, revoke-principal,
// purge-principal) — each POSTs {name} to its endpoint.
func runVaultNameOp(action, path, pastTense string, args []string) int {
	fs := flag.NewFlagSet("vault "+action, flag.ContinueOnError)
	apiURL := fs.String("api-url", "", "Daemon API base URL")
	token := fs.String("token", "", "Vault admin token (or DUCTILE_VAULT_TOKEN)")
	name := fs.String("name", "", "Target name")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *name == "" {
		fmt.Fprintf(os.Stderr, "vault %s: --name is required\n", action)
		return 1
	}
	if _, err := vaultAPIPost(*apiURL, resolveVaultToken(*token), path, map[string]any{"name": *name}); err != nil {
		fmt.Fprintf(os.Stderr, "vault %s: %v\n", action, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "%s %q.\n", pastTense, *name)
	return 0
}

// runVaultRollPrincipal rolls every auto-pattern secret a principal holds and
// reports which were rolled and which manual secrets were skipped.
func runVaultRollPrincipal(args []string) int {
	fs := flag.NewFlagSet("vault roll-principal", flag.ContinueOnError)
	apiURL := fs.String("api-url", "", "Daemon API base URL")
	token := fs.String("token", "", "Vault admin token (or DUCTILE_VAULT_TOKEN)")
	name := fs.String("name", "", "Principal name")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "vault roll-principal: --name is required")
		return 1
	}
	respBody, err := vaultAPIPost(*apiURL, resolveVaultToken(*token), "/vault/principal/roll",
		map[string]any{"name": *name})
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault roll-principal: %v\n", err)
		return 1
	}
	var resp struct {
		Rolled  []string `json:"rolled"`
		Skipped []string `json:"skipped"`
	}
	_ = json.Unmarshal(respBody, &resp)
	fmt.Fprintf(os.Stderr, "Rolled %d secret(s) for principal %q.\n", len(resp.Rolled), *name)
	if len(resp.Skipped) > 0 {
		fmt.Fprintf(os.Stderr, "Skipped (manual — roll each with `vault roll`): %s\n", strings.Join(resp.Skipped, ", "))
	}
	return 0
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
