package secrets

import (
	"fmt"
	"os"

	"filippo.io/age"
)

// Keyring holds the age identities the gateway uses to decrypt config at load.
// It is an explicit value passed by the loader, not a global. A nil or empty
// Keyring means "no decryption configured" — plaintext config still loads, but
// any encrypted file encountered is a hard error (see Decrypt).
type Keyring struct {
	identities []age.Identity
}

// Empty reports whether the keyring holds no identities.
func (k *Keyring) Empty() bool {
	return k == nil || len(k.identities) == 0
}

// Decrypt decrypts age ciphertext with the keyring's identities. An empty
// keyring is an error: encountering an encrypted file with no key configured
// must fail loudly, never silently skip.
func (k *Keyring) Decrypt(ciphertext []byte) ([]byte, error) {
	if k.Empty() {
		return nil, fmt.Errorf("encountered age-encrypted data but no decryption key is configured " +
			"(set secrets.age_key_file or DUCTILE_AGE_KEY_FILE)")
	}
	return Decrypt(ciphertext, k.identities)
}

// Recipients derives the age recipients (public keys) that correspond to the
// keyring's identities, for self-encryption (encrypt to the same key that
// decrypts). Used by the vault to re-encrypt on save. Identities that cannot
// yield a recipient (non-X25519) are a hard error rather than a silent skip.
func (k *Keyring) Recipients() ([]age.Recipient, error) {
	if k.Empty() {
		return nil, fmt.Errorf("keyring: no identities to derive recipients from")
	}
	recipients := make([]age.Recipient, 0, len(k.identities))
	for _, id := range k.identities {
		x, ok := id.(*age.X25519Identity)
		if !ok {
			return nil, fmt.Errorf("keyring: identity of type %T cannot derive a recipient", id)
		}
		recipients = append(recipients, x.Recipient())
	}
	return recipients, nil
}

// LoadKeyringFromFile reads an age identity (key) file, enforces restrictive
// permissions (no group/other access), and parses the identities it contains.
//
// The permission check is the floor that makes filesystem isolation meaningful:
// a key file readable by other users defeats the point, so we refuse to load it.
func LoadKeyringFromFile(path string) (*Keyring, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("age key file %q: %w", path, err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return nil, fmt.Errorf("age key file %q has insecure permissions %#o: "+
			"must not be readable by group or others (chmod 600 %s)", path, mode, path)
	}
	// #nosec G304 -- key path is operator-controlled local input.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("age key file %q: %w", path, err)
	}
	ids, err := ParseIdentities(data)
	if err != nil {
		return nil, fmt.Errorf("age key file %q: %w", path, err)
	}
	return &Keyring{identities: ids}, nil
}
