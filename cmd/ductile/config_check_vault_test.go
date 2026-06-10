package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeBootstrapConfig writes the canonical from-scratch bootstrap shape: API
// enabled, ZERO api.auth.tokens (the credential ladder mints the first token
// after boot). Returns the config path.
func writeBootstrapConfig(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	cfgYAML := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
  unconfined: true
state:
  path: ` + filepath.Join(dir, "state.db") + `
api:
  enabled: true
  listen: 127.0.0.1:0
`
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath
}

// #133: the CLI validation path must reach the same admission verdict as the
// daemon. A genesis-vault, zero-token config boots the management posture
// (#129) — so `config check` and every validateConfigAtPath caller must accept
// it rather than demand api.auth.tokens the ladder has not minted yet.
func TestValidateConfigAtPathAcceptsBootstrapWithVault(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeBootstrapConfig(t, tmp)
	genesisVaultForTest(t, tmp)

	result, exitCode, err := validateConfigAtPath(configPath)
	if err != nil {
		t.Fatalf("validateConfigAtPath: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit 0 for genesis-vault bootstrap config (daemon boots it into management posture), got %d: %+v", exitCode, result)
	}
}

// The #94/#119 strictness floor: with NO vault on disk there is nothing to
// bootstrap a token from, so an enabled API with zero tokens stays rejected.
func TestValidateConfigAtPathRejectsTokenlessWithoutVault(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeBootstrapConfig(t, tmp)

	result, exitCode, err := validateConfigAtPath(configPath)
	// The loader itself rejects this shape (validate, hasVault=false), so the
	// refusal surfaces as a load error with exit 1 — either channel is a pass,
	// exit 0 is the regression.
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit for tokenless config with no vault, got 0 (err=%v): %+v", err, result)
	}
}
