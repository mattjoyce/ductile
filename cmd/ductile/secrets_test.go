package main

import (
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/mattjoyce/ductile/internal/secrets"
)

func TestSecretsKeygenWritesProtectedIdentity(t *testing.T) {
	out := filepath.Join(t.TempDir(), "age.key")
	if code := runSecretsKeygen([]string{"--out", out}); code != 0 {
		t.Fatalf("keygen exit = %d, want 0", code)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat identity: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("identity mode = %#o, want 0600", mode)
	}
	// The written identity must be loadable as a keyring (parses cleanly).
	if _, err := secrets.LoadKeyringFromFile(out); err != nil {
		t.Fatalf("generated identity not loadable: %v", err)
	}
}

func TestSecretsEncryptRotateRoundTrip(t *testing.T) {
	dir := t.TempDir()

	oldID, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("gen old: %v", err)
	}
	newID, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("gen new: %v", err)
	}

	oldKey := filepath.Join(dir, "old.key")
	newKey := filepath.Join(dir, "new.key")
	for path, id := range map[string]*age.X25519Identity{oldKey: oldID, newKey: newID} {
		if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
			t.Fatalf("write key: %v", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatalf("chmod: %v", err)
		}
	}

	plainPath := filepath.Join(dir, "tokens.yaml")
	if err := os.WriteFile(plainPath, []byte("tokens:\n  - name: x\n    key: secret\n"), 0o600); err != nil {
		t.Fatalf("write plaintext: %v", err)
	}
	cipherPath := filepath.Join(dir, "tokens.yaml.age")

	// encrypt to the old recipient
	if code := runSecretsEncrypt([]string{
		"--recipient", oldID.Recipient().String(),
		"--in", plainPath,
		"--out", cipherPath,
	}); code != 0 {
		t.Fatalf("encrypt exit = %d, want 0", code)
	}
	ct, _ := os.ReadFile(cipherPath)
	if !secrets.IsEncrypted(ct) {
		t.Fatal("encrypt output not encrypted")
	}

	// rotate to the new recipient using the old key to decrypt
	if code := runSecretsRotate([]string{
		"--key", oldKey,
		"--recipient", newID.Recipient().String(),
		"--file", cipherPath,
	}); code != 0 {
		t.Fatalf("rotate exit = %d, want 0", code)
	}

	rotated, _ := os.ReadFile(cipherPath)
	// the new key decrypts it...
	newKr, err := secrets.LoadKeyringFromFile(newKey)
	if err != nil {
		t.Fatalf("load new key: %v", err)
	}
	got, err := newKr.Decrypt(rotated)
	if err != nil {
		t.Fatalf("new key cannot decrypt rotated file: %v", err)
	}
	if string(got) != "tokens:\n  - name: x\n    key: secret\n" {
		t.Fatalf("rotated plaintext mismatch: %q", got)
	}
	// ...and the old key no longer does.
	oldKr, _ := secrets.LoadKeyringFromFile(oldKey)
	if _, err := oldKr.Decrypt(rotated); err == nil {
		t.Fatal("old key still decrypts rotated file; rotation did not change recipients")
	}
}

func TestSecretsRotatePreservesInputOnBadKey(t *testing.T) {
	dir := t.TempDir()
	goodID, _ := secrets.GenerateIdentity()
	wrongID, _ := secrets.GenerateIdentity()

	wrongKey := filepath.Join(dir, "wrong.key")
	if err := os.WriteFile(wrongKey, []byte(wrongID.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	_ = os.Chmod(wrongKey, 0o600)

	ct, err := secrets.Encrypt([]byte("payload"), []age.Recipient{goodID.Recipient()})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	cipherPath := filepath.Join(dir, "tokens.yaml.age")
	if err := os.WriteFile(cipherPath, ct, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if code := runSecretsRotate([]string{
		"--key", wrongKey,
		"--recipient", wrongID.Recipient().String(),
		"--file", cipherPath,
	}); code == 0 {
		t.Fatal("rotate with wrong key exit = 0, want failure")
	}

	// Input must be untouched: still decryptable by the original good key.
	after, _ := os.ReadFile(cipherPath)
	if string(after) != string(ct) {
		t.Fatal("rotate clobbered the input file on failure")
	}
}
