package vault

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattjoyce/ductile/internal/secrets"
	"gopkg.in/yaml.v3"
)

// Vault owns the lifecycle of one store: the resident in-memory model plus the
// persistence boundary (decrypt-on-load, re-encrypt + atomic write on change).
// It is the single owner of the model — there is no global. The caller holds the
// Vault and supervises its errors; the Vault itself never panics.
//
// lastYAML is the canonical (marshalled) plaintext of the last persisted/loaded
// state. Save compares against it to write only on change. We compare the
// *plaintext* form, not ciphertext: age is non-deterministic, so equal models
// would still produce different ciphertext.
type Vault struct {
	path     string
	keyring  *secrets.Keyring
	store    *Store
	lastYAML []byte
}

// New wraps an in-memory store with a path and keyring. It performs no I/O — call
// Save to persist. A nil store becomes an empty one.
func New(path string, kr *secrets.Keyring, s *Store) *Vault {
	if s == nil {
		s = NewStore()
	}
	return &Vault{path: path, keyring: kr, store: s}
}

// Load reads, decrypts, and parses an existing vault blob. It fails closed:
// a missing file, a non-encrypted file, a decrypt failure, or malformed YAML are
// all errors — never a silently-empty store (which would erase every secret).
func Load(path string, kr *secrets.Keyring) (*Vault, error) {
	// #nosec G304 -- vault path is operator-controlled local input.
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("vault: read %q: %w", path, err)
	}
	if !secrets.IsEncrypted(ciphertext) {
		return nil, fmt.Errorf("vault: %q is not age-encrypted", path)
	}
	plaintext, err := kr.Decrypt(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("vault: decrypt %q: %w", path, err)
	}
	var s Store
	if err := yaml.Unmarshal(plaintext, &s); err != nil {
		return nil, fmt.Errorf("vault: parse %q: %w", path, err)
	}
	if s.Secrets == nil {
		s.Secrets = make(map[string]*Secret)
	}
	if s.Principals == nil {
		s.Principals = make(map[string]*Principal)
	}
	// Canonicalise the baseline so a no-op Save after Load does not rewrite due
	// to formatting differences between the on-disk bytes and yaml.Marshal.
	canonical, err := yaml.Marshal(&s)
	if err != nil {
		return nil, fmt.Errorf("vault: canonicalise %q: %w", path, err)
	}
	return &Vault{path: path, keyring: kr, store: &s, lastYAML: canonical}, nil
}

// Store returns the resident in-memory model. Mutations are not persisted until
// Save.
func (v *Vault) Store() *Store { return v.store }

// Path returns the on-disk location of the blob.
func (v *Vault) Path() string { return v.path }

// Save serialises the model and, only if it differs from the last persisted
// state, re-encrypts the whole document and writes it atomically. Returns nil
// without touching disk when nothing changed.
func (v *Vault) Save() error {
	current, err := yaml.Marshal(v.store)
	if err != nil {
		return fmt.Errorf("vault: serialise: %w", err)
	}
	if v.lastYAML != nil && bytes.Equal(current, v.lastYAML) {
		return nil // write-on-change: nothing to do
	}
	recipients, err := v.keyring.Recipients()
	if err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	ciphertext, err := secrets.Encrypt(current, recipients)
	if err != nil {
		return fmt.Errorf("vault: encrypt: %w", err)
	}
	if err := writeFileAtomic(v.path, ciphertext); err != nil {
		return fmt.Errorf("vault: write %q: %w", v.path, err)
	}
	v.lastYAML = current
	return nil
}

// writeFileAtomic writes data to a temp file in the same directory, fsyncs it,
// then renames over the target. A crash leaves either the old blob or the new
// one — never a truncated file. No .bak is kept: a stale backup of an encrypted
// store could outlive a recipient roll and remain decryptable by a retired key.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".vault-*")
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
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
