package main

import (
	"bytes"
	"context"
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
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
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
	case "get":
		return runVaultGet(actionArgs)
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
	case "rotate-admin-token":
		return runVaultRotateAdminToken(actionArgs)
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
	_, _ = fmt.Fprintln(w, "  rotate-key  Rotate the vault's age identity (mints + re-encrypts)       [--config]")
	_, _ = fmt.Fprintln(w, "  rotate-admin-token  Rotate the management-API admin token in place        [--config]")
	_, _ = fmt.Fprintln(w, "              (mints a fresh token, prints it once; the old token stops working)")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Local, key-holding READ (holds the age key; read-only, so the daemon MAY be running):")
	_, _ = fmt.Fprintln(w, "  get         Print ONE secret's value to stdout (never over the API)        [--config --name]")
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

// getSecretValue returns the value of a named, readable secret, or an error
// explaining why it cannot be read: unknown name, revoked (value cleared), or a
// pattern: auto secret not yet minted by a roll. Kept separate from runVaultGet's
// I/O so the read policy is unit-testable without a key or a blob on disk.
func getSecretValue(store *vault.Store, name string) (string, error) {
	if vault.IsReservedSecret(name) {
		// The admin token is the management-API credential, not a deliverable
		// secret. `vault get` must never print it (least-surprise): it is shown
		// once at `vault init` and otherwise lives only in the encrypted blob.
		return "", fmt.Errorf("secret %q is reserved (the management-API credential) and is never printed by 'vault get'", name)
	}
	sec, ok := store.Secret(name)
	if !ok {
		return "", fmt.Errorf("unknown secret %q", name)
	}
	if sec.Status == vault.StatusRevoked {
		return "", fmt.Errorf("secret %q is revoked (value cleared)", name)
	}
	if sec.Value == "" {
		return "", fmt.Errorf("secret %q has no value yet (pattern %s; run 'vault roll' to mint)", name, sec.Pattern)
	}
	return sec.Value, nil
}

// runVaultGet prints ONE secret's value, read LOCALLY. It is a key-holding op: it
// resolves and decrypts the on-disk vault blob with the age key, so it never goes
// over the daemon's management API — that API is value-free by design (it never
// reads secret values back out). The operator already holds the key, so a local
// read leaks nothing they could not `age -d` themselves.
//
// Unlike the write-side key-touching ops (rotate-key/import) it is READ-ONLY, so
// it does NOT take the daemon PID lock: the blob is written atomically, so reading
// it while the daemon is running is safe. The value goes to stdout (pipeable, e.g.
// T=$(ductile vault get --name x)); all notices go to stderr.
func runVaultGet(args []string) int {
	fs := flag.NewFlagSet("vault get", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to the ductile config dir (default: discover)")
	name := fs.String("name", "", "Secret name")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "vault get: --name is required")
		return 1
	}

	cfg, configDir, err := loadBackupConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault get: %v\n", err)
		return 1
	}
	vaultPath := config.ResolveVaultPath(configDir, cfg)
	keyPath := config.ResolveAgeKeyPath(configDir, cfg)
	if keyPath == "" {
		fmt.Fprintln(os.Stderr,
			"vault get: no age key file resolved (set secrets.age_key_file or DUCTILE_AGE_KEY_FILE)")
		return 1
	}
	kr, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault get: load key: %v\n", err)
		return 1
	}
	v, err := vault.Load(vaultPath, kr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault get: load vault: %v\n", err)
		return 1
	}

	value, err := getSecretValue(v.Store(), *name)
	if err != nil {
		// Record the value-touching attempt on a reserved secret — someone tried
		// to read the admin token locally. Other errors (unknown/revoked/no value)
		// touch no value, so they are not audited as reads.
		if vault.IsReservedSecret(*name) {
			auditVaultRead(cfg, *name, "denied", "reserved secret; never printed by vault get")
		}
		fmt.Fprintf(os.Stderr, "vault get: %v\n", err)
		return 1
	}

	auditVaultRead(cfg, *name, "ok", "local key-holder read")

	// Soft warning if the value would land in a terminal's scrollback.
	if fi, statErr := os.Stdout.Stat(); statErr == nil && fi.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprintln(os.Stderr,
			"vault get: warning — writing a secret value to a terminal; redirect or capture it instead.")
	}
	fmt.Fprintf(os.Stderr, "Read value of secret %q locally (age key-holder).\n", *name)
	fmt.Println(value)
	return 0
}

// auditVaultRead best-effort records a local `vault get` to the vault audit log,
// so a value-touching read leaves a trace alongside the API-side mutations. It
// records the secret NAME and an outcome only — never the value. Best-effort per
// the audit fault model (state/vault_audit.go): a read is never failed because
// the row could not be written. It deliberately does NOT bootstrap the state DB:
// on a host where no daemon has run yet there is nothing to audit into, and a
// read must not create one as a side effect.
func auditVaultRead(cfg *config.Config, name, outcome, detail string) {
	if cfg == nil || cfg.State.Path == "" {
		return
	}
	if _, err := os.Stat(cfg.State.Path); err != nil {
		return // no state DB yet — skip rather than create one on a read
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := storage.OpenSQLite(ctx, cfg.State.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault get: warning — read not audited (open state db: %v)\n", err)
		return
	}
	defer func() { _ = db.Close() }()
	if err := state.NewStore(db).AppendVaultAudit(ctx, state.VaultAuditEvent{
		Op:         "read",
		SecretName: name,
		Actor:      "cli",
		Outcome:    outcome,
		Detail:     detail,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "vault get: warning — read not audited: %v\n", err)
	}
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

// runVaultRotateAdminToken rotates the vault's reserved management-API admin token
// LOCALLY. Rotation surfaces a fresh token value, and the management API is
// value-free by design (it never emits secret values over HTTP) — so, like
// init/rotate-key, this is a local, key-touching op, never over the API: it holds
// the age key, mints a new token, persists the blob, and prints the token ONCE to
// stdout. It requires the daemon to be down (acquires the PID lock, refusing if
// held) so the live daemon's resident token and the on-disk blob cannot diverge.
// The previous token stops authenticating the moment the blob is saved; capture
// the new one and update any API clients (DUCTILE_VAULT_TOKEN) before restarting.
func runVaultRotateAdminToken(args []string) int {
	fs := flag.NewFlagSet("vault rotate-admin-token", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to the ductile config dir (default: discover)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	cfg, configDir, err := loadBackupConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault rotate-admin-token: %v\n", err)
		return 1
	}

	// Sole-writer: the daemon is the only writer of the vault. Rotation is local and
	// key-touching, so refuse if the daemon holds the PID lock; holding it for the op
	// also stops a daemon starting mid-rotation.
	pidLock, err := lock.AcquirePIDLock(getPIDLockPath(cfg))
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"vault rotate-admin-token: the daemon appears to be running (could not take the lock): %v\n"+
				"Stop the daemon before rotating the admin token — key-touching ops are local.\n", err)
		return 1
	}
	defer func() { _ = pidLock.Release() }()

	vaultPath := config.ResolveVaultPath(configDir, cfg)
	keyPath := config.ResolveAgeKeyPath(configDir, cfg)
	if keyPath == "" {
		fmt.Fprintln(os.Stderr,
			"vault rotate-admin-token: no age key file resolved (set secrets.age_key_file or DUCTILE_AGE_KEY_FILE)")
		return 1
	}

	kr, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault rotate-admin-token: load key: %v\n", err)
		return 1
	}
	v, err := vault.Load(vaultPath, kr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault rotate-admin-token: load vault: %v\n", err)
		return 1
	}

	newToken, err := v.RotateAdminToken(time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault rotate-admin-token: %v\n", err)
		return 1
	}

	// Best-effort audit, alongside the API-side mutations — the op + outcome only,
	// never the value (parity with the value-free API audit).
	auditVaultRotateAdminToken(cfg)

	fmt.Fprintln(os.Stderr, "Rotated the vault admin token; the previous token no longer authenticates.")
	fmt.Fprintln(os.Stderr, "New admin token (shown once — store it in 0600 custody and update any API clients):")
	// Soft warning if the value would land in a terminal's scrollback.
	if fi, statErr := os.Stdout.Stat(); statErr == nil && fi.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprintln(os.Stderr,
			"vault rotate-admin-token: warning — writing a secret value to a terminal; redirect or capture it instead.")
	}
	// The token itself goes to stdout so it can be captured/piped cleanly.
	fmt.Println(newToken)
	return 0
}

// auditVaultRotateAdminToken best-effort records an admin-token rotation to the
// vault audit log — the op and outcome only, NEVER the value, mirroring the API's
// value-free audit. Best-effort per the audit fault model (state/vault_audit.go):
// rotation is never failed because the row could not be written, and it does NOT
// bootstrap the state DB (a host where no daemon has run has nothing to audit into).
func auditVaultRotateAdminToken(cfg *config.Config) {
	if cfg == nil || cfg.State.Path == "" {
		return
	}
	if _, err := os.Stat(cfg.State.Path); err != nil {
		return // no state DB yet — skip rather than create one on a rotation
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := storage.OpenSQLite(ctx, cfg.State.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault rotate-admin-token: warning — not audited (open state db: %v)\n", err)
		return
	}
	defer func() { _ = db.Close() }()
	if err := state.NewStore(db).AppendVaultAudit(ctx, state.VaultAuditEvent{
		Op:         "rotate-admin-token",
		SecretName: vault.AdminTokenSecret,
		Actor:      "cli",
		Outcome:    "ok",
		Detail:     "local key-holder rotation",
	}); err != nil {
		fmt.Fprintf(os.Stderr, "vault rotate-admin-token: warning — not audited: %v\n", err)
	}
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
	// Empty default: the daemon defaults pattern to manual on CREATE and leaves it
	// unchanged on UPDATE — so a metadata edit can't silently flip an auto secret.
	pattern := fs.String("pattern", "", "Provisioning pattern: manual|auto (default manual on create; left unchanged on update)")
	principalsCSV := fs.String("principal", "", "Comma-separated principals authorized to receive the secret. Omit to leave existing grants; pass an empty string to clear them.")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "vault set: --name is required")
		return 1
	}

	tok := resolveVaultToken(*token)

	// Read the value from stdin so the secret never appears on argv. An empty value
	// means "leave unchanged" on update (the value is roll-only; use 'vault roll').
	valueBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault set: read value from stdin: %v\n", err)
		return 1
	}
	value := strings.TrimRight(string(valueBytes), "\r\n")

	// Distinguish --principal absent (leave grants) from given (replace; empty clears).
	var principals *[]string
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "principal" {
			return
		}
		list := []string{}
		for _, p := range strings.Split(*principalsCSV, ",") {
			if p = strings.TrimSpace(p); p != "" {
				list = append(list, p)
			}
		}
		principals = &list
	})

	if err := doVaultSet(*apiURL, tok, *name, value, principals, *pattern); err != nil {
		fmt.Fprintf(os.Stderr, "vault set: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Set secret %q via the daemon.\n", *name)
	return 0
}

// doVaultSet POSTs a secret to the daemon's /vault/secret endpoint. principals is
// nil to leave existing grants, or non-nil to replace them (empty clears); pattern
// "" is omitted so the daemon defaults (create) or leaves (update) it.
func doVaultSet(apiURL, token, name, value string, principals *[]string, pattern string) error {
	body := map[string]any{"name": name, "value": value}
	if principals != nil {
		body["authorized_principals"] = *principals
	}
	if pattern != "" {
		body["pattern"] = pattern
	}
	_, err := vaultAPIPost(apiURL, token, "/vault/secret", body)
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
