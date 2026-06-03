package vault

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

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
	// mu makes the Vault the goroutine-safe sole-writer owner of its model:
	// guarded reads (Compose) take RLock, the writer (SetSecret) takes Lock and
	// persists under it. The Store stays a pure, lock-free model; concurrency is
	// the owner's concern. Direct Store() access bypasses the lock and is for
	// single-threaded genesis and tests only — it never escapes into the running
	// daemon: runtime read consumers take a Snapshot (a deep copy under RLock) and
	// writers go through the guarded methods (SetSecret, SetManualBatch, and the
	// lifecycle ops). The remaining cross-process writer hole — a second process
	// loading and re-saving the blob while the daemon serves — is closed outside
	// the mutex by requiring those local key-touching CLIs (vault init/import/
	// rotate-key) to hold the daemon PID lock, i.e. run only while it is stopped.
	mu       sync.RWMutex
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

// Store returns the resident in-memory model directly, aliasing the live
// pointer. Mutations are not persisted until Save. This bypasses the owner's
// lock, so it is for single-threaded genesis and tests ONLY. Runtime read
// consumers must take a Snapshot (an independent deep copy) and writers must use
// the guarded methods (SetSecret, SetManualBatch, the lifecycle ops) — never
// reach the live model through Store() while the daemon may be serving.
func (v *Vault) Store() *Store { return v.store }

// Snapshot returns an independent deep copy of the resident model, taken under a
// read lock. It is the safe read path for callers that need the pure Store shape
// (e.g. the load-time secret graft) without aliasing the live model: mutating
// the returned copy never touches the vault, and holding it past the lock is
// harmless because it is no longer shared.
func (v *Vault) Snapshot() (*Store, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return cloneStore(v.store)
}

// cloneStore deep-copies a Store via a YAML round-trip — the same canonical
// serialisation the vault persists, so the copy is structurally identical and
// independent (maps, the *Secret/*Principal values, and their slices are all
// fresh). Using the marshal path means the clone never rots as the model gains
// fields.
func cloneStore(s *Store) (*Store, error) {
	data, err := yaml.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("vault: snapshot serialise: %w", err)
	}
	var c Store
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("vault: snapshot parse: %w", err)
	}
	if c.Secrets == nil {
		c.Secrets = make(map[string]*Secret)
	}
	if c.Principals == nil {
		c.Principals = make(map[string]*Principal)
	}
	return &c, nil
}

// ManualSecret is one (name, value) pair for a batched manual import.
type ManualSecret struct {
	Name  string
	Value string
}

// ImportFailure reports a single entry SetManualBatch could not upsert, with the
// reason, so the caller can flag it without aborting the rest of the batch.
type ImportFailure struct {
	Name   string
	Reason string
}

// SetManualBatch upserts a batch of manual-pattern secrets (operator-supplied
// values, no grants) as ONE guarded critical section followed by a single Save,
// so a `vault import` is atomic with respect to concurrent readers and writes
// the blob exactly once. Per-entry validation failures are returned (name +
// reason) without aborting the batch; a Save failure rolls the whole batch back
// to the last persisted state. now stamps each upsert. It is the guarded
// replacement for reaching the live model through Store().
func (v *Vault) SetManualBatch(entries []ManualSecret, now time.Time) ([]ImportFailure, error) {
	var failures []ImportFailure
	err := v.mutate(func(s *Store) error {
		for _, e := range entries {
			if setErr := s.SetSecret(e.Name, e.Value, nil, PatternManual, now); setErr != nil {
				failures = append(failures, ImportFailure{Name: e.Name, Reason: setErr.Error()})
			}
		}
		return nil // per-entry failures are reported, not fatal to the batch
	})
	if err != nil {
		return nil, err
	}
	return failures, nil
}

// Compose resolves a principal's authorized secrets under a read lock, so it is
// safe to call concurrently with other reads and with SetSecret. It is the
// daemon's spawn-time read path (satisfies dispatch.SecretComposer).
func (v *Vault) Compose(principal string) (Composition, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.store.Compose(principal)
}

// SetSecret is the guarded sole-writer mutation: it upserts a secret and
// persists the blob as one critical section, so concurrent Compose readers
// never observe a half-applied or unpersisted change.
//
// Atomicity contract: a validation failure leaves the model untouched (the pure
// SetSecret validates before mutating). If the in-memory mutation succeeds but
// the on-disk Save fails, the model is rolled back to the last persisted state
// so memory and disk never diverge.
func (v *Vault) SetSecret(name, value string, authorizedPrincipals []string, pattern string, now time.Time) error {
	return v.mutate(func(s *Store) error {
		return s.SetSecret(name, value, authorizedPrincipals, pattern, now)
	})
}

// mutate runs a pure model mutation under the write lock and persists it,
// atomically: fn either errors before mutating (the pure ops validate first, so
// the model is untouched) or succeeds, after which the blob is Saved. If the
// Save fails, the in-memory model is rolled back to the last persisted state so
// memory and disk never diverge. fn must not perform I/O — only mutate s.
func (v *Vault) mutate(fn func(*Store) error) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if err := fn(v.store); err != nil {
		return err // validated before mutating: model unchanged
	}
	if err := v.Save(); err != nil {
		if rbErr := v.restoreFromLastYAML(); rbErr != nil {
			return fmt.Errorf("%w (rollback also failed: %v)", err, rbErr)
		}
		return err
	}
	return nil
}

// AuthenticateAdmin reports whether presented matches the vault's resident
// admin token (constant-time), under a read lock. This is the management-API
// credential check: the admin token is minted by genesis (vault init), lives
// inside the vault, and is rotatable by a normal write — so vault-write
// authorization never depends on a config-defined token. A revoked admin token
// authenticates no one.
func (v *Vault) AuthenticateAdmin(presented string) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	sec, ok := v.store.Secrets[AdminTokenSecret]
	if !ok || sec.Status != StatusActive {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(sec.Value)) == 1
}

// restoreFromLastYAML rebuilds the resident model from the last persisted
// canonical plaintext, reverting an unpersisted mutation. Callers hold v.mu.
func (v *Vault) restoreFromLastYAML() error {
	if v.lastYAML == nil {
		return fmt.Errorf("vault: cannot roll back: no persisted baseline")
	}
	var s Store
	if err := yaml.Unmarshal(v.lastYAML, &s); err != nil {
		return fmt.Errorf("vault: roll back parse: %w", err)
	}
	if s.Secrets == nil {
		s.Secrets = make(map[string]*Secret)
	}
	if s.Principals == nil {
		s.Principals = make(map[string]*Principal)
	}
	v.store = &s
	return nil
}

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
