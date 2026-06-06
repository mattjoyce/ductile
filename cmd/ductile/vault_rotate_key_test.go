package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattjoyce/ductile/internal/lock"
	"github.com/mattjoyce/ductile/internal/secrets"
	"github.com/mattjoyce/ductile/internal/vault"
)

// rotateKeyConfigDir builds a minimal config dir with an age key and an
// initialised vault, returning the dir, the key path, and the vault path.
func rotateKeyConfigDir(t *testing.T) (dir, keyPath, vaultPath string) {
	t.Helper()
	dir = t.TempDir()
	keyPath = filepath.Join(dir, "age.key")
	vaultPath = filepath.Join(dir, "vault.age")

	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("gen identity: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	configYAML := "" +
		"service:\n  tick_interval: 60s\n  log_level: info\n  allow_symlinks: true\n" +
		"state:\n  path: " + filepath.Join(dir, "state.db") + "\n" +
		"plugin_roots:\n  - " + pluginsDir + "\n" +
		"plugins: {}\n" +
		"secrets:\n  age_key_file: age.key\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if code := runVaultInit([]string{"--vault", vaultPath, "--key", keyPath}); code != 0 {
		t.Fatalf("vault init exit = %d", code)
	}
	return dir, keyPath, vaultPath
}

func TestRunVaultRotateKeyDaemonDown(t *testing.T) {
	dir, keyPath, vaultPath := rotateKeyConfigDir(t)

	// Capture the OLD keyring before rotation overwrites the key file.
	oldKR, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("load old keyring: %v", err)
	}

	if code := runVaultRotateKey([]string{"--config", dir}); code != 0 {
		t.Fatalf("rotate-key exit = %d, want 0", code)
	}

	// Old key can no longer decrypt; the rewritten key file can.
	if _, err := vault.Load(vaultPath, oldKR); err == nil {
		t.Error("old key must NOT decrypt the rotated vault")
	}
	newKR, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("load rotated keyring: %v", err)
	}
	if _, err := vault.Load(vaultPath, newKR); err != nil {
		t.Errorf("rotated key must decrypt the vault: %v", err)
	}
}

func TestRunVaultRotateKeyRefusesWhileDaemonRunning(t *testing.T) {
	dir, keyPath, vaultPath := rotateKeyConfigDir(t)

	origKR, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}

	// Simulate the running daemon by holding its PID lock.
	pidLock, err := lock.AcquirePIDLock(filepath.Join(dir, "state.pid"))
	if err != nil {
		t.Fatalf("acquire pid lock: %v", err)
	}
	defer func() { _ = pidLock.Release() }()

	if code := runVaultRotateKey([]string{"--config", dir}); code == 0 {
		t.Fatal("rotate-key must REFUSE while the daemon holds the PID lock")
	}

	// The vault must be untouched — still decryptable by the original key.
	if _, err := vault.Load(vaultPath, origKR); err != nil {
		t.Errorf("refused rotation must leave the vault untouched: %v", err)
	}
}
