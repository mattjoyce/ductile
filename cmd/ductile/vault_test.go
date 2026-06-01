package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattjoyce/ductile/internal/secrets"
	"github.com/mattjoyce/ductile/internal/vault"
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

func TestRunVaultImport(t *testing.T) {
	key := writeKeyFile(t)
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.age")
	if code := runVaultInit([]string{"--vault", vaultPath, "--key", key}); code != 0 {
		t.Fatalf("init exit = %d", code)
	}

	tokensPath := filepath.Join(dir, "tokens.yaml")
	if err := os.WriteFile(tokensPath, []byte(
		"tokens:\n  - name: gh_webhook\n    key: literal-secret\n  - name: env_one\n    key: ${IMPORT_TEST_VAR}\n",
	), 0o600); err != nil {
		t.Fatalf("write tokens.yaml: %v", err)
	}

	kr, err := secrets.LoadKeyringFromFile(key)
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}

	// Without --resolve-env: literal imports, env-pointer is flagged (not imported).
	if code := runVaultImport([]string{"--vault", vaultPath, "--key", key, "--tokens", tokensPath}); code != 0 {
		t.Fatalf("import exit = %d", code)
	}
	v, err := vault.Load(vaultPath, kr)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if sec, ok := v.Store().Secret("gh_webhook"); !ok || sec.Value != "literal-secret" {
		t.Errorf("literal secret not imported: %+v ok=%v", sec, ok)
	}
	if _, ok := v.Store().Secret("env_one"); ok {
		t.Error("env-pointer imported without --resolve-env; should have been flagged")
	}

	// With --resolve-env and the variable set: the pointer resolves and imports.
	t.Setenv("IMPORT_TEST_VAR", "resolved-val")
	if code := runVaultImport([]string{"--vault", vaultPath, "--key", key, "--tokens", tokensPath, "--resolve-env"}); code != 0 {
		t.Fatalf("import --resolve-env exit = %d", code)
	}
	v2, err := vault.Load(vaultPath, kr)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if sec, ok := v2.Store().Secret("env_one"); !ok || sec.Value != "resolved-val" {
		t.Errorf("resolved env-pointer not imported: %+v ok=%v", sec, ok)
	}
}
