package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mattjoyce/ductile/internal/secrets"
)

// testKeyring writes a fresh age identity to a 0600 file and loads it.
func testKeyring(t *testing.T) *secrets.Keyring {
	t.Helper()
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	path := filepath.Join(t.TempDir(), "age.key")
	if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod key: %v", err)
	}
	kr, err := secrets.LoadKeyringFromFile(path)
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}
	return kr
}

func populatedStore() *Store {
	s := NewStore()
	s.Principals["withings"] = &Principal{Kind: KindPlugin, Status: StatusActive}
	s.Principals["core"] = &Principal{Kind: KindGateway, Status: StatusActive}
	s.Secrets["withings_api"] = &Secret{
		Value:                "top-secret-value",
		AuthorizedPrincipals: []string{"withings", "health_dash"},
		Status:               StatusActive,
		Pattern:              PatternManual,
		RollCount:            2,
		CreatedAt:            "2026-06-01T00:00:00Z",
		Description:          "Withings API token",
	}
	return s
}

func TestSaveLoadRoundTripEmpty(t *testing.T) {
	kr := testKeyring(t)
	path := filepath.Join(t.TempDir(), "vault.age")

	if err := New(path, kr, NewStore()).Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	v, err := Load(path, kr)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(v.Store().Secrets) != 0 || len(v.Store().Principals) != 0 {
		t.Fatalf("expected empty store, got %+v", v.Store())
	}
}

func TestSaveLoadRoundTripPopulated(t *testing.T) {
	kr := testKeyring(t)
	path := filepath.Join(t.TempDir(), "vault.age")
	want := populatedStore()

	if err := New(path, kr, want).Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	v, err := Load(path, kr)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(v.Store(), want) {
		t.Fatalf("round-trip mismatch:\n got: %+v\nwant: %+v", v.Store().Secrets["withings_api"], want.Secrets["withings_api"])
	}
}

func TestLoadMissingFileFails(t *testing.T) {
	kr := testKeyring(t)
	if _, err := Load(filepath.Join(t.TempDir(), "absent.age"), kr); err == nil {
		t.Fatal("Load of missing file succeeded; want fail-closed error")
	}
}

func TestLoadPlaintextFileFails(t *testing.T) {
	kr := testKeyring(t)
	path := filepath.Join(t.TempDir(), "vault.age")
	if err := os.WriteFile(path, []byte("secrets: {}\nprincipals: {}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path, kr); err == nil {
		t.Fatal("Load of a non-encrypted file succeeded; want error (fail-closed)")
	}
}

func TestLoadWrongKeyFails(t *testing.T) {
	krA := testKeyring(t)
	krB := testKeyring(t) // different identity
	path := filepath.Join(t.TempDir(), "vault.age")
	if err := New(path, krA, populatedStore()).Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := Load(path, krB); err == nil {
		t.Fatal("Load with the wrong key succeeded; want hard failure, never an empty store")
	}
}

func TestSaveProducesEncryptedBlobNoLeakNoTemp(t *testing.T) {
	kr := testKeyring(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.age")
	if err := New(path, kr, populatedStore()).Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !secrets.IsEncrypted(raw) {
		t.Fatal("blob on disk is not age-encrypted")
	}
	if bytes.Contains(raw, []byte("top-secret-value")) {
		t.Fatal("plaintext secret value leaked into the on-disk blob")
	}
	if bytes.Contains(raw, []byte("withings_api")) {
		t.Fatal("secret name leaked into the on-disk blob (metadata must be hidden at rest)")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "vault.age" {
			t.Fatalf("unexpected leftover file in vault dir: %q (no temp/.bak should remain)", e.Name())
		}
	}
}

func TestSaveWriteOnChange(t *testing.T) {
	kr := testKeyring(t)
	path := filepath.Join(t.TempDir(), "vault.age")
	v := New(path, kr, populatedStore())
	if err := v.Save(); err != nil {
		t.Fatalf("save 1: %v", err)
	}
	first, _ := os.ReadFile(path)

	// No change -> no rewrite. Because age is non-deterministic, a rewrite would
	// produce different ciphertext; identical bytes prove no write happened.
	if err := v.Save(); err != nil {
		t.Fatalf("save 2 (no change): %v", err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatal("Save rewrote the blob despite no model change (write-on-change violated)")
	}

	// Mutate -> rewrite.
	v.Store().Secrets["new"] = &Secret{Value: "x", Status: StatusActive, Pattern: PatternManual}
	if err := v.Save(); err != nil {
		t.Fatalf("save 3 (changed): %v", err)
	}
	third, _ := os.ReadFile(path)
	if bytes.Equal(second, third) {
		t.Fatal("Save did not rewrite the blob after a model change")
	}

	// And the change persists across a reload.
	reloaded, err := Load(path, kr)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := reloaded.Store().Secrets["new"]; !ok {
		t.Fatal("mutation did not persist across reload")
	}
}
