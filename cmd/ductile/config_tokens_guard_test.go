package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/secrets"
)

// TestTokensFileGuardsRefuseEncrypted pins the ADR §7/§8 safety gap: the legacy,
// non-age-aware config-token mutators must refuse an age-encrypted tokens file
// (read would yield ciphertext; write would clobber it) and redirect to the vault.
func TestTokensFileGuardsRefuseEncrypted(t *testing.T) {
	key := writeKeyFile(t)
	kr, err := secrets.LoadKeyringFromFile(key)
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	recipients, err := kr.Recipients()
	if err != nil {
		t.Fatalf("recipients: %v", err)
	}
	ciphertext, err := secrets.Encrypt([]byte("tokens:\n  - name: x\n    key: y\n"), recipients)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	if err := os.WriteFile(path, ciphertext, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, loadErr := loadTokensFile(path)
	if loadErr == nil || !strings.Contains(loadErr.Error(), "vault") {
		t.Fatalf("loadTokensFile must refuse encrypted with a vault redirect, got %v", loadErr)
	}

	writeErr := writeTokensFile(path, &config.TokensFileConfig{})
	if writeErr == nil || !strings.Contains(writeErr.Error(), "vault") {
		t.Fatalf("writeTokensFile must refuse clobbering encrypted, got %v", writeErr)
	}

	// The encrypted file must be untouched (no plaintext clobber, no .bak).
	if after, _ := os.ReadFile(path); !secrets.IsEncrypted(after) {
		t.Error("encrypted tokens file was modified despite the guard")
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("a .bak of the encrypted store was written; guard should prevent any write")
	}
}
