package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattjoyce/ductile/internal/lock"
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

// writeImportTokens drops a tokens.yaml (one literal, one ${ENV} pointer) into
// dir and returns its path.
func writeImportTokens(t *testing.T, dir string) string {
	t.Helper()
	tokensPath := filepath.Join(dir, "tokens.yaml")
	if err := os.WriteFile(tokensPath, []byte(
		"tokens:\n  - name: gh_webhook\n    key: literal-secret\n  - name: env_one\n    key: ${IMPORT_TEST_VAR}\n",
	), 0o600); err != nil {
		t.Fatalf("write tokens.yaml: %v", err)
	}
	return tokensPath
}

func TestRunVaultImport(t *testing.T) {
	// Import is now config-driven (like rotate-key): it resolves the vault + key
	// from config and holds the daemon PID lock.
	dir, keyPath, vaultPath := rotateKeyConfigDir(t)
	tokensPath := writeImportTokens(t, dir)

	kr, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}

	// Without --resolve-env: literal imports, env-pointer is flagged (not imported).
	if code := runVaultImport([]string{"--config", dir, "--tokens", tokensPath}); code != 0 {
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
	if code := runVaultImport([]string{"--config", dir, "--tokens", tokensPath, "--resolve-env"}); code != 0 {
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

func TestRunVaultImportRefusesWhileDaemonRunning(t *testing.T) {
	dir, keyPath, vaultPath := rotateKeyConfigDir(t)
	tokensPath := writeImportTokens(t, dir)

	origKR, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}

	// Simulate the running daemon by holding its PID lock — import must refuse so
	// it cannot lost-update the blob the daemon owns.
	pidLock, err := lock.AcquirePIDLock(filepath.Join(dir, "state.pid"))
	if err != nil {
		t.Fatalf("acquire pid lock: %v", err)
	}
	defer func() { _ = pidLock.Release() }()

	if code := runVaultImport([]string{"--config", dir, "--tokens", tokensPath}); code == 0 {
		t.Fatal("import must REFUSE while the daemon holds the PID lock")
	}

	// The refused import must not have written: gh_webhook is absent and the vault
	// is still decryptable by the original key.
	v, err := vault.Load(vaultPath, origKR)
	if err != nil {
		t.Fatalf("refused import must leave the vault untouched: %v", err)
	}
	if _, ok := v.Store().Secret("gh_webhook"); ok {
		t.Error("refused import must not have written gh_webhook")
	}
}
