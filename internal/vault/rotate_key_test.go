package vault

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"filippo.io/age"
	"github.com/mattjoyce/ductile/internal/secrets"
)

// writeKeyAt writes a fresh age identity to path (0600) and returns it.
func writeKeyAt(t *testing.T, path string) *age.X25519Identity {
	t.Helper()
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return id
}

// rotateFixture saves a populated vault under a fresh key and returns the loaded
// owner plus the key/vault paths.
func rotateFixture(t *testing.T) (v *Vault, keyPath, vaultPath string, oldID *age.X25519Identity, want *Store) {
	t.Helper()
	dir := t.TempDir()
	keyPath = filepath.Join(dir, "age.key")
	vaultPath = filepath.Join(dir, "vault.age")
	oldID = writeKeyAt(t, keyPath)
	oldKR, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("load old keyring: %v", err)
	}
	want = populatedStore()
	if err := New(vaultPath, oldKR, want).Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	v, err = Load(vaultPath, oldKR)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return v, keyPath, vaultPath, oldID, want
}

func TestRotateKeyReencryptsToFreshIdentity(t *testing.T) {
	v, keyPath, vaultPath, oldID, want := rotateFixture(t)
	oldKR, _ := secrets.LoadKeyringFromFile(keyPath)

	newRecipient, err := v.RotateKey(keyPath)
	if err != nil {
		t.Fatalf("rotate key: %v", err)
	}
	if newRecipient == "" {
		t.Fatal("rotate must return the new public recipient")
	}
	if _, err := age.ParseX25519Recipient(newRecipient); err != nil {
		t.Errorf("returned recipient is not a valid age recipient: %v", err)
	}
	if newRecipient == oldID.Recipient().String() {
		t.Error("rotate must mint a NEW recipient, got the old one back")
	}

	// Old key must no longer decrypt the rotated blob (old recipient retired).
	if _, err := Load(vaultPath, oldKR); err == nil {
		t.Error("old key must NOT decrypt the rotated blob")
	}

	// The rewritten key file decrypts, and the model is preserved byte-for-byte.
	newKR, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("load rewritten keyring: %v", err)
	}
	reloaded, err := Load(vaultPath, newKR)
	if err != nil {
		t.Fatalf("new key must decrypt the rotated blob: %v", err)
	}
	if !reflect.DeepEqual(reloaded.Store(), want) {
		t.Errorf("model not preserved across rotation:\n got %+v\nwant %+v", reloaded.Store(), want)
	}

	// New key file keeps restrictive permissions.
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("rewritten key file mode = %#o, want 0600", info.Mode().Perm())
	}
}

// verifyDecrypts is the safety gate: it must accept only the right key AND the
// right plaintext, so a bad mint can never pass before the old key is retired.
func TestVerifyDecryptsGate(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "v.age")

	idA, _ := secrets.GenerateIdentity()
	idB, _ := secrets.GenerateIdentity()
	want := []byte("the-model-bytes")

	ct, err := secrets.Encrypt(want, []age.Recipient{idA.Recipient()})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if err := os.WriteFile(blob, ct, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := verifyDecrypts(blob, idA, want); err != nil {
		t.Errorf("right key + right plaintext must verify, got %v", err)
	}
	if err := verifyDecrypts(blob, idB, want); err == nil {
		t.Error("wrong key must fail verification")
	}
	if err := verifyDecrypts(blob, idA, []byte("different")); err == nil {
		t.Error("mismatched plaintext must fail verification")
	}
}

// After rotation the resident owner must persist under the NEW key, so a
// subsequent mutating Save cannot silently re-encrypt to the retired old key.
func TestRotateKeyAdoptsNewKeyringForLaterSaves(t *testing.T) {
	v, keyPath, vaultPath, _, _ := rotateFixture(t)
	oldKR, _ := secrets.LoadKeyringFromFile(keyPath)

	if _, err := v.RotateKey(keyPath); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// Mutate and persist through the owner; it must encrypt to the NEW key.
	if err := v.SetSecret("late", "v", []string{"core"}, PatternManual, testTime); err != nil {
		t.Fatalf("set after rotate: %v", err)
	}
	if _, err := Load(vaultPath, oldKR); err == nil {
		t.Error("a Save after rotate must NOT be decryptable by the old key")
	}
	newKR, _ := secrets.LoadKeyringFromFile(keyPath)
	if _, err := Load(vaultPath, newKR); err != nil {
		t.Errorf("a Save after rotate must be decryptable by the new key: %v", err)
	}
}
