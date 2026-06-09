package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/secrets"
	"github.com/mattjoyce/ductile/internal/vault"
)

func writeBootstrapWebhookConfigDir(t *testing.T, withVault bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	cfgYAML := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
state:
  path: ` + filepath.Join(dir, "state.db") + `
secrets:
  age_key_file: age.key
  vault_file: vault.age
include:
  - webhooks.yaml
api:
  enabled: true
  listen: 127.0.0.1:0
plugins:
  echo:
    enabled: true
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfgYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	webhooksYAML := `
webhooks:
  endpoints:
    - path: /webhook/github
      plugin: echo
      secret_ref: github_webhook_secret
      signature_header: X-Hub-Signature-256
`
	if err := os.WriteFile(filepath.Join(dir, "webhooks.yaml"), []byte(webhooksYAML), 0o644); err != nil {
		t.Fatalf("write webhooks: %v", err)
	}
	// Scope files are hash-verified on load.
	if _, err := GenerateChecksumsWithReport(dir, []string{"webhooks.yaml"}, false); err != nil {
		t.Fatalf("generate checksums: %v", err)
	}
	if withVault {
		id, err := secrets.GenerateIdentity()
		if err != nil {
			t.Fatalf("generate identity: %v", err)
		}
		keyPath := filepath.Join(dir, "age.key")
		if err := os.WriteFile(keyPath, []byte(id.String()+"\n"), 0o600); err != nil {
			t.Fatalf("write age key: %v", err)
		}
		kr, err := secrets.LoadKeyringFromFile(keyPath)
		if err != nil {
			t.Fatalf("load keyring: %v", err)
		}
		if _, _, err := vault.Init(filepath.Join(dir, "vault.age"), kr, time.Now()); err != nil {
			t.Fatalf("vault init: %v", err)
		}
	}
	return dir
}

// #138: a from-scratch config that DECLARES webhooks must reach the posture
// decision — the management posture exists to mint the very secret_ref the
// hard error complained about. The bootstrap condition (api enabled, zero
// tokens, vault present — DecideBootPosture's own predicate) downgrades the
// unminted secret_ref to a warning at load.
func TestLoadBootstrapConfigWithUnmintedWebhookSecretRef(t *testing.T) {
	dir := writeBootstrapWebhookConfigDir(t, true)
	cfg, owner, err := LoadWithVault(dir)
	if err != nil {
		t.Fatalf("bootstrap config with declared webhooks must load (the daemon mints the secret after boot): %v", err)
	}
	if owner == nil {
		t.Fatal("expected a vault owner from genesis")
	}
	if got := DecideBootPosture(cfg, owner != nil); got != PostureManagementOnly {
		t.Fatalf("posture = %v, want management-only", got)
	}
}

// The strictness floor: with NO vault there is no posture that can mint the
// secret — the unresolved secret_ref stays a hard load error.
func TestLoadWebhookSecretRefStillStrictWithoutVault(t *testing.T) {
	dir := writeBootstrapWebhookConfigDir(t, false)
	if _, err := Load(dir); err == nil {
		t.Fatal("vault-less config with an unresolvable webhook secret_ref must refuse to load")
	}
}
