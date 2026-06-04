package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filippo.io/age"
	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/secrets"
)

// samePath reports whether two paths point at the same file. It compares
// absolute, symlink-resolved, cleaned paths so /tmp vs /private/tmp or a
// relative --file can't slip past an equality check.
func samePath(a, b string) bool {
	ra, ea := filepath.Abs(a)
	rb, eb := filepath.Abs(b)
	if ea != nil || eb != nil {
		return false
	}
	if r, err := filepath.EvalSymlinks(ra); err == nil {
		ra = r
	}
	if r, err := filepath.EvalSymlinks(rb); err == nil {
		rb = r
	}
	return filepath.Clean(ra) == filepath.Clean(rb)
}

// vaultGuardPath returns the configured vault blob path for the #31 guard, or ""
// when no ductile config is discoverable (so `secrets rotate` still works as a
// generic age tool outside a ductile context). Best-effort: any load error means
// "no guard", never a hard failure.
func vaultGuardPath(configPath string) string {
	cfg, configDir, err := loadBackupConfig(configPath)
	if err != nil {
		return ""
	}
	return config.ResolveVaultPath(configDir, cfg)
}

func runSecretsNoun(args []string) int {
	if len(args) < 1 {
		printSecretsNounHelp(os.Stderr)
		return 1
	}
	if isHelpToken(args[0]) {
		printSecretsNounHelp(os.Stdout)
		return 0
	}

	action := args[0]
	actionArgs := args[1:]

	switch action {
	case "keygen":
		return runSecretsKeygen(actionArgs)
	case "encrypt":
		return runSecretsEncrypt(actionArgs)
	case "rotate":
		return runSecretsRotate(actionArgs)
	case "help":
		printSecretsNounHelp(os.Stdout)
		return 0
	default:
		// #nosec G705 -- stderr output is plain text, not HTML.
		fmt.Fprintf(os.Stderr, "Unknown secrets action: %s\n", action)
		printSecretsNounHelp(os.Stderr)
		return 1
	}
}

func printSecretsNounHelp(w *os.File) {
	_, _ = fmt.Fprintln(w, "Usage: ductile secrets <action>")
	_, _ = fmt.Fprintln(w, "Actions:")
	_, _ = fmt.Fprintln(w, "  keygen   Generate an age identity (private key) and its recipient (public key)")
	_, _ = fmt.Fprintln(w, "  encrypt  Encrypt a plaintext file to one or more recipients")
	_, _ = fmt.Fprintln(w, "  rotate   Re-encrypt an encrypted file under a new recipient set")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "These commands operate on config bundles (e.g. tokens.yaml), NOT the vault.")
	_, _ = fmt.Fprintln(w, "To rotate the vault's own key use 'ductile vault rotate-key'.")
}

// runSecretsKeygen generates a new age identity. The private identity is written
// to --out (mode 0600) or stdout; the public recipient is always printed to
// stderr so it is visible even when the identity is captured from stdout.
func runSecretsKeygen(args []string) int {
	fs := flag.NewFlagSet("secrets keygen", flag.ContinueOnError)
	out := fs.String("out", "", "Path to write the identity (private key); default stdout")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	id, err := secrets.GenerateIdentity()
	if err != nil {
		fmt.Fprintf(os.Stderr, "keygen: %v\n", err)
		return 1
	}

	identityText := fmt.Sprintf("# created by ductile secrets keygen\n# public key: %s\n%s\n",
		id.Recipient().String(), id.String())

	if *out == "" {
		fmt.Print(identityText)
	} else {
		if err := os.WriteFile(*out, []byte(identityText), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "keygen: write identity: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "Wrote identity to %s (mode 0600)\n", *out)
	}
	// Public recipient to stderr so `--out` users still see it.
	fmt.Fprintf(os.Stderr, "Public recipient: %s\n", id.Recipient().String())
	return 0
}

// runSecretsEncrypt encrypts plaintext (from --in or stdin) to the given
// recipients and writes armored ciphertext to --out or stdout.
func runSecretsEncrypt(args []string) int {
	fs := flag.NewFlagSet("secrets encrypt", flag.ContinueOnError)
	var recipientFlags stringSlice
	fs.Var(&recipientFlags, "recipient", "Recipient public key (age1...); repeatable")
	recipientsFile := fs.String("recipients-file", "", "Path to a file of recipients, one per line")
	in := fs.String("in", "", "Path to plaintext input; default stdin")
	out := fs.String("out", "", "Path to write ciphertext; default stdout")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	recipients, err := collectRecipients(recipientFlags, *recipientsFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "encrypt: %v\n", err)
		return 1
	}

	plaintext, err := readInputOrStdin(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "encrypt: read input: %v\n", err)
		return 1
	}

	ciphertext, err := secrets.Encrypt(plaintext, recipients)
	if err != nil {
		fmt.Fprintf(os.Stderr, "encrypt: %v\n", err)
		return 1
	}

	return writeOutputOrStdout(*out, ciphertext, "encrypt")
}

// runSecretsRotate decrypts an encrypted file with the given key, then
// re-encrypts it under a new recipient set. The original file is rewritten only
// after the new ciphertext is produced and staged via a temp file + rename, so a
// failure to decrypt or encrypt never destroys the input.
func runSecretsRotate(args []string) int {
	fs := flag.NewFlagSet("secrets rotate", flag.ContinueOnError)
	keyFile := fs.String("key", "", "Path to the age identity (private key) that can decrypt the file")
	var recipientFlags stringSlice
	fs.Var(&recipientFlags, "recipient", "New recipient public key (age1...); repeatable")
	recipientsFile := fs.String("recipients-file", "", "Path to a file of new recipients, one per line")
	file := fs.String("file", "", "Path to the encrypted file to rotate in place")
	configPath := fs.String("config", "", "ductile config dir (default: discover); refuses if --file is the vault blob")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *keyFile == "" || *file == "" {
		fmt.Fprintln(os.Stderr, "rotate: --key and --file are required")
		return 1
	}

	// #31: secrets rotate is a generic age tool, but rewriting the vault blob with
	// it re-encrypts to a recipient set the daemon's resident model never sees —
	// the next daemon write silently reverts it (the silent-revert footgun). Refuse
	// before touching the file and point to the blessed path. Best-effort: outside a
	// ductile config context vaultGuardPath is "" and the generic tool still works.
	if vp := vaultGuardPath(*configPath); vp != "" && samePath(*file, vp) {
		fmt.Fprintf(os.Stderr,
			"rotate: %s is the ductile vault — use 'ductile vault rotate-key' instead "+
				"(secrets rotate would re-encrypt it to a recipient set the running daemon never sees, "+
				"and the next daemon write would silently revert it)\n", *file)
		return 1
	}

	recipients, err := collectRecipients(recipientFlags, *recipientsFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rotate: %v\n", err)
		return 1
	}

	kr, err := secrets.LoadKeyringFromFile(*keyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rotate: %v\n", err)
		return 1
	}

	// #nosec G304 -- file path is operator-controlled local input.
	ciphertext, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rotate: read %s: %v\n", *file, err)
		return 1
	}
	if !secrets.IsEncrypted(ciphertext) {
		fmt.Fprintf(os.Stderr, "rotate: %s is not age-encrypted\n", *file)
		return 1
	}

	plaintext, err := kr.Decrypt(ciphertext)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rotate: decrypt %s: %v (input left unchanged)\n", *file, err)
		return 1
	}

	rotated, err := secrets.Encrypt(plaintext, recipients)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rotate: re-encrypt: %v (input left unchanged)\n", err)
		return 1
	}

	if err := writeFileAtomic(*file, rotated); err != nil {
		fmt.Fprintf(os.Stderr, "rotate: write %s: %v\n", *file, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Rotated %s to %d recipient(s)\n", *file, len(recipients))
	return 0
}

func collectRecipients(flags stringSlice, recipientsFile string) ([]age.Recipient, error) {
	var recipients []age.Recipient
	for _, r := range flags {
		rec, err := age.ParseX25519Recipient(r)
		if err != nil {
			return nil, fmt.Errorf("parse recipient %q: %w", r, err)
		}
		recipients = append(recipients, rec)
	}
	if recipientsFile != "" {
		// #nosec G304 -- recipients file path is operator-controlled local input.
		data, err := os.ReadFile(recipientsFile)
		if err != nil {
			return nil, fmt.Errorf("read recipients file: %w", err)
		}
		fileRecipients, err := secrets.ParseRecipients(data)
		if err != nil {
			return nil, err
		}
		recipients = append(recipients, fileRecipients...)
	}
	if len(recipients) == 0 {
		return nil, fmt.Errorf("no recipients (use --recipient or --recipients-file)")
	}
	return recipients, nil
}

func readInputOrStdin(path string) ([]byte, error) {
	if path == "" {
		return io.ReadAll(os.Stdin)
	}
	// #nosec G304 -- input path is operator-controlled local input.
	return os.ReadFile(path)
}

func writeOutputOrStdout(path string, data []byte, label string) int {
	if path == "" {
		if _, err := os.Stdout.Write(data); err != nil {
			fmt.Fprintf(os.Stderr, "%s: write stdout: %v\n", label, err)
			return 1
		}
		return 0
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "%s: write %s: %v\n", label, path, err)
		return 1
	}
	return 0
}

// writeFileAtomic writes data to a temp file in the same directory then renames
// it over the target, so a partial write never leaves a truncated file.
//
// It deliberately does NOT reuse writeFileAtomicWithBackup (config_manage.go):
// rotation is often done precisely because an old recipient's key is
// compromised, and a .bak of the pre-rotation ciphertext would still be
// decryptable by that old key — defeating the rotation. No stale copy is left.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ductile-rotate-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
