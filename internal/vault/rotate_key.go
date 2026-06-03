package vault

import (
	"bytes"
	"fmt"
	"os"
	"slices"

	"filippo.io/age"
	"github.com/mattjoyce/ductile/internal/secrets"
	"gopkg.in/yaml.v3"
)

// RotateKey rotates the vault's age identity: it mints a fresh identity,
// re-encrypts the store to it, writes the new identity to keyFilePath, and
// retires the old key — so the blob at rest is readable only by the new key. It
// returns the new public recipient so the operator can back up the freshly
// written key.
//
// It is a LOCAL, key-touching operation: the caller ensures the daemon is down
// (the CLI holds the PID lock), consistent with `vault init` and #8's rule that
// key-touching ops never run over the management API.
//
// Safety is a dual-recipient bridge plus verify-before-retire. The blob is
// transiently encrypted to {old, new} so every on-disk (key, blob) pair stays
// decryptable across a crash, and the new key is proven to decrypt the new blob
// BEFORE the old key is retired (a bad mint aborts with the old key still intact,
// rather than bricking the vault). No backup of the old key is kept — a retired
// key lying around defeats the rotation.
func (v *Vault) RotateKey(keyFilePath string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	oldRecipients, err := v.keyring.Recipients()
	if err != nil {
		return "", fmt.Errorf("vault: rotate: derive current recipients: %w", err)
	}

	newID, err := secrets.GenerateIdentity()
	if err != nil {
		return "", fmt.Errorf("vault: rotate: mint identity: %w", err)
	}
	newRecipient := newID.Recipient()

	plaintext, err := yaml.Marshal(v.store)
	if err != nil {
		return "", fmt.Errorf("vault: rotate: serialise: %w", err)
	}

	// Phase 1 — bridge: encrypt to {old, new} so either key can read the blob.
	bridge := slices.Concat(oldRecipients, []age.Recipient{newRecipient})
	if err := v.reencryptTo(plaintext, bridge); err != nil {
		return "", fmt.Errorf("vault: rotate: write bridged blob: %w", err)
	}

	// Phase 2 — verify the new key alone decrypts the on-disk blob before the old
	// key is retired. On failure, restore the blob to {old} only and abort.
	if err := verifyDecrypts(v.path, newID, plaintext); err != nil {
		if rbErr := v.reencryptTo(plaintext, oldRecipients); rbErr != nil {
			return "", fmt.Errorf("vault: rotate: %w (rollback to old recipients also failed: %v)", err, rbErr)
		}
		return "", fmt.Errorf("vault: rotate: %w", err)
	}

	// Phase 3 — commit the new identity to the boot key path (atomic, 0600, no
	// backup). The blob is still {old, new}, so a crash here leaves the new key
	// able to read it.
	keyText := fmt.Sprintf("# created by ductile vault rotate-key\n# public key: %s\n%s\n",
		newRecipient.String(), newID.String())
	if err := writeFileAtomic(keyFilePath, []byte(keyText)); err != nil {
		return "", fmt.Errorf("vault: rotate: write new key %q: %w", keyFilePath, err)
	}

	// Phase 4 — finalise: retire the old recipient so the blob is readable only
	// by the new key.
	if err := v.reencryptTo(plaintext, []age.Recipient{newRecipient}); err != nil {
		return "", fmt.Errorf("vault: rotate: finalise blob (new key is live at %q — re-run or restore): %w", keyFilePath, err)
	}

	// Adopt the new identity as the resident keyring + baseline, so any later
	// Save persists under the new key (never silently re-encrypting to the old).
	newKR, err := secrets.NewKeyring(newID)
	if err != nil {
		return "", fmt.Errorf("vault: rotate: adopt keyring: %w", err)
	}
	v.keyring = newKR
	v.lastYAML = plaintext
	return newRecipient.String(), nil
}

// reencryptTo encrypts plaintext to recipients and atomically writes it to the
// vault blob. Callers hold v.mu.
func (v *Vault) reencryptTo(plaintext []byte, recipients []age.Recipient) error {
	ciphertext, err := secrets.Encrypt(plaintext, recipients)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	if err := writeFileAtomic(v.path, ciphertext); err != nil {
		return fmt.Errorf("write %q: %w", v.path, err)
	}
	return nil
}

// verifyDecrypts reads the blob at path back and asserts that id alone decrypts
// it to want — the proof that the new key works before the old one is retired.
func verifyDecrypts(path string, id age.Identity, want []byte) error {
	// #nosec G304 -- vault path is operator-controlled local input.
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("verify: read back %q: %w", path, err)
	}
	got, err := secrets.Decrypt(ciphertext, []age.Identity{id})
	if err != nil {
		return fmt.Errorf("verify: new key cannot decrypt the new blob: %w", err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("verify: decrypted blob does not match the model")
	}
	return nil
}
