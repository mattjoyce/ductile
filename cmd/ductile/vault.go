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
	"github.com/mattjoyce/ductile/internal/lock"
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
	case "rotate-key":
		return runVaultRotateKey(actionArgs)
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
	_, _ = fmt.Fprintln(w, "Usage: ductile vault <action> [flags]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Vault writes have two classes. Run 'ductile vault <action> --help' for full flags.")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Local, key-touching (hold the age key; the daemon must be STOPPED):")
	_, _ = fmt.Fprintln(w, "  init        Genesis: create a new vault (core + nonce + admin token)   [--vault --key]")
	_, _ = fmt.Fprintln(w, "  import      Migrate tokens.yaml entries into an existing vault         [--config --tokens --resolve-env]")
	_, _ = fmt.Fprintln(w, "  rotate-key  Rotate the vault's age identity (mints + re-encrypts)       [--config]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Keyless API clients (no age key; POST to the running daemon, the sole writer):")
	_, _ = fmt.Fprintln(w, "  Common flags: --api-url, --token (or DUCTILE_VAULT_TOKEN), --name")
	_, _ = fmt.Fprintln(w, "  register-principal Register a deliver-to principal                     [--kind plugin|consumer|gateway]")
	_, _ = fmt.Fprintln(w, "  set                Set a secret (value from stdin, never argv)         [--pattern manual|auto --principal a,b]")
	_, _ = fmt.Fprintln(w, "  roll               Roll a secret's value (manual: stdin; auto: minted)")
	_, _ = fmt.Fprintln(w, "  revoke             Revoke a secret (terminal; clears the value)")
	_, _ = fmt.Fprintln(w, "  revoke-principal   Revoke a principal (its secrets stop being delivered)")
	_, _ = fmt.Fprintln(w, "  purge-principal    Remove a principal and strip all its grants")
	_, _ = fmt.Fprintln(w, "  roll-principal     Roll every auto secret a principal holds")
}

// runVaultRotateKey rotates the vault's age identity LOCALLY. Rotation is
// key-touching, so per #8 it never goes over the management API; instead it
// requires the daemon to be down, enforced by acquiring the daemon's PID lock
// (refuse if held). It resolves the daemon's EXACT vault + key paths so the new
// key lands where the daemon next boots from, then delegates the atomic
// dual-recipient bridge + verify-before-retire to vault.RotateKey.
func runVaultRotateKey(args []string) int {
	fs := flag.NewFlagSet("vault rotate-key", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to the ductile config dir (default: discover)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	cfg, configDir, err := loadBackupConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault rotate-key: %v\n", err)
		return 1
	}

	// Sole-writer: the daemon is the only writer of the vault. Rotation is local
	// and key-touching, so refuse if the daemon holds the PID lock; holding it for
	// the op also stops a daemon starting mid-rotation.
	pidLock, err := lock.AcquirePIDLock(getPIDLockPath(cfg))
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"vault rotate-key: the daemon appears to be running (could not take the lock): %v\n"+
				"Stop the daemon before rotating the vault key — key-touching ops are local.\n", err)
		return 1
	}
	defer func() { _ = pidLock.Release() }()

	vaultPath := config.ResolveVaultPath(configDir, cfg)
	keyPath := config.ResolveAgeKeyPath(configDir, cfg)
	if keyPath == "" {
		fmt.Fprintln(os.Stderr,
			"vault rotate-key: no age key file resolved (set secrets.age_key_file or DUCTILE_AGE_KEY_FILE)")
		return 1
	}

	kr, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault rotate-key: load current key: %v\n", err)
		return 1
	}
	v, err := vault.Load(vaultPath, kr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault rotate-key: load vault: %v\n", err)
		return 1
	}

	newRecipient, err := v.RotateKey(keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault rotate-key: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "Rotated the vault key; the previous key is retired.\n")
	fmt.Fprintf(os.Stderr, "New key written to %s (mode 0600) — BACK IT UP NOW "+
		"(e.g. to your password manager). It is the only key that can decrypt the vault.\n", keyPath)
	fmt.Fprintf(os.Stderr, "New public recipient: %s\n", newRecipient)
	return 0
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
// never over the API — like init and rotate-key. Because it is a second process
// loading and re-saving the blob, it would lost-update a running daemon, so it
// follows rotate-key's safety envelope: it resolves the daemon's EXACT vault +
// key paths from config and holds the daemon PID lock for the op, refusing if the
// daemon is up. The actual upsert goes through the guarded SetManualBatch (one
// lock + one Save), not the live Store(). Literal values move in; ${ENV} pointers
// are flagged unless --resolve-env is given, in which case resolvable ones import
// their resolved value and the rest are flagged for manual re-provisioning.
func runVaultImport(args []string) int {
	fs := flag.NewFlagSet("vault import", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to the ductile config dir (default: discover)")
	tokensPath := fs.String("tokens", "", "Path to the tokens.yaml to import from")
	resolveEnv := fs.Bool("resolve-env", false, "Import the resolved value of ${ENV} entries instead of flagging them")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *tokensPath == "" {
		fmt.Fprintln(os.Stderr, "vault import: --tokens is required")
		return 1
	}

	cfg, configDir, err := loadBackupConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault import: %v\n", err)
		return 1
	}

	// Sole-writer: the daemon is the only writer of the vault. Import is local and
	// key-touching, so refuse if the daemon holds the PID lock; holding it for the
	// op also stops a daemon starting mid-import.
	pidLock, err := lock.AcquirePIDLock(getPIDLockPath(cfg))
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"vault import: the daemon appears to be running (could not take the lock): %v\n"+
				"Stop the daemon before importing — key-touching ops are local.\n", err)
		return 1
	}
	defer func() { _ = pidLock.Release() }()

	vaultPath := config.ResolveVaultPath(configDir, cfg)
	keyPath := config.ResolveAgeKeyPath(configDir, cfg)
	if keyPath == "" {
		fmt.Fprintln(os.Stderr,
			"vault import: no age key file resolved (set secrets.age_key_file or DUCTILE_AGE_KEY_FILE)")
		return 1
	}

	kr, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault import: %v\n", err)
		return 1
	}
	v, err := vault.Load(vaultPath, kr)
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

	batch := make([]vault.ManualSecret, 0, len(plan.Imported))
	for _, s := range plan.Imported {
		batch = append(batch, vault.ManualSecret{Name: s.Name, Value: s.Value})
	}
	failures, err := v.SetManualBatch(batch, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault import: save: %v\n", err)
		return 1
	}
	for _, f := range failures {
		plan.Flagged = append(plan.Flagged, config.FlaggedSecret{Name: f.Name, Reason: "could not register: " + f.Reason})
	}
	imported := len(batch) - len(failures)

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
