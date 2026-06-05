package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattjoyce/ductile/internal/secrets"
)

func writeKeyFile(t *testing.T) string {
	t.Helper()
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("gen identity: %v", err)
	}
	path := filepath.Join(t.TempDir(), "age.key")
	if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return path
}

func TestRunVaultInit(t *testing.T) {
	key := writeKeyFile(t)
	vaultPath := filepath.Join(t.TempDir(), "vault.age")

	if code := runVaultInit([]string{"--vault", vaultPath, "--key", key}); code != 0 {
		t.Fatalf("vault init exit = %d, want 0", code)
	}
	if _, err := os.Stat(vaultPath); err != nil {
		t.Fatalf("vault blob not written: %v", err)
	}

	// Refuses to clobber.
	if code := runVaultInit([]string{"--vault", vaultPath, "--key", key}); code == 0 {
		t.Fatal("re-init succeeded; must refuse to clobber")
	}
}

func TestRunVaultInitRequiresFlags(t *testing.T) {
	if code := runVaultInit(nil); code == 0 {
		t.Fatal("vault init without flags succeeded; --vault and --key are required")
	}
}

