package secrets

import (
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
)

func writeKeyFile(t *testing.T, id *age.X25519Identity, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "age.key")
	if err := os.WriteFile(path, []byte(id.String()+"\n"), mode); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	// os.WriteFile honours umask; force the intended mode.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return path
}

func TestLoadKeyringFromFile(t *testing.T) {
	t.Parallel()
	id := newTestIdentity(t)
	path := writeKeyFile(t, id, 0o600)

	kr, err := LoadKeyringFromFile(path)
	if err != nil {
		t.Fatalf("LoadKeyringFromFile: %v", err)
	}
	if kr.Empty() {
		t.Fatal("keyring empty after loading a valid key")
	}

	ciphertext, err := Encrypt([]byte("hello"), []age.Recipient{id.Recipient()})
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := kr.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("keyring Decrypt: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q want hello", got)
	}
}

func TestLoadKeyringRejectsInsecurePermissions(t *testing.T) {
	t.Parallel()
	id := newTestIdentity(t)
	path := writeKeyFile(t, id, 0o644)
	if _, err := LoadKeyringFromFile(path); err == nil {
		t.Fatal("LoadKeyringFromFile accepted a world-readable key; want rejection")
	}
}

func TestLoadKeyringMissingFile(t *testing.T) {
	t.Parallel()
	if _, err := LoadKeyringFromFile(filepath.Join(t.TempDir(), "absent.key")); err == nil {
		t.Fatal("LoadKeyringFromFile of a missing file succeeded; want error")
	}
}

func TestEmptyKeyringDecryptFails(t *testing.T) {
	t.Parallel()
	var kr *Keyring
	if _, err := kr.Decrypt([]byte("x")); err == nil {
		t.Fatal("nil keyring Decrypt succeeded; want error")
	}
	empty := &Keyring{}
	if _, err := empty.Decrypt([]byte("x")); err == nil {
		t.Fatal("empty keyring Decrypt succeeded; want error")
	}
}
