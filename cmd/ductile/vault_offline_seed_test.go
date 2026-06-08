package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/secrets"
	"github.com/mattjoyce/ductile/internal/vault"
)

// Offline vault seed (#128). A from-scratch vault-native deploy with the API
// enabled is un-bootstrappable without an offline seed: the API bearer token
// must be a vault secret_ref resolved fail-closed at boot (#94), but the live
// `vault set` goes through the daemon, which won't boot until the secret exists.
// doVaultSetOffline breaks that cycle. These tests pin the property that matters:
// after an offline seed, the boot-time config load resolves the token; without
// it, boot still fails closed.

// writeSeedConfig writes a minimal config dir whose API token is a vault-only
// secret_ref, plus a freshly-genesis'd vault + age key. Returns the config.yaml
// path and the age key path.
func writeSeedConfig(t *testing.T) (configPath, vaultPath, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	write("config.yaml",
		"service:\n  name: seed-test\n"+
			"state:\n  path: ./state/ductile.db\n"+
			"plugin_roots:\n  - ./plugins\n"+
			"secrets:\n  age_key_file: age.key\n  vault_file: vault.age\n"+
			"include:\n  - api.yaml\n")
	write("api.yaml",
		"api:\n  enabled: true\n  listen: \"127.0.0.1:0\"\n"+
			"  auth:\n    tokens:\n      - secret_ref: ductile-api-admin\n        scopes: [\"*\"]\n")

	keyPath = filepath.Join(dir, "age.key")
	vaultPath = filepath.Join(dir, "vault.age")
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	kr, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	if _, _, err := vault.Init(vaultPath, kr, time.Now()); err != nil {
		t.Fatalf("vault init: %v", err)
	}
	return filepath.Join(dir, "config.yaml"), vaultPath, keyPath
}

// TestOfflineSeedBootstrapsAPIToken is the headline property: an offline-seeded
// API token granted to `core` makes the boot-time config load succeed and the
// token resolve — and without the seed, boot fails closed (the #94/#119 guard).
func TestOfflineSeedBootstrapsAPIToken(t *testing.T) {
	configPath, vaultPath, keyPath := writeSeedConfig(t)

	// Before the seed: boot fails closed — the secret_ref is not in the vault.
	if _, _, err := config.LoadWithVault(configPath); err == nil {
		t.Fatal("expected boot to fail closed before the API token is seeded")
	}

	// Seed the API token offline, granted to the gateway principal `core`.
	if err := doVaultSetOffline(vaultPath, keyPath, "ductile-api-admin", "boot-token-xyz", []string{"core"}, ""); err != nil {
		t.Fatalf("offline seed: %v", err)
	}

	// After the seed: boot loads cleanly and the API token resolves to its value.
	cfg, _, err := config.LoadWithVault(configPath)
	if err != nil {
		t.Fatalf("boot must succeed after seeding the API token, got: %v", err)
	}
	resolved, err := config.ResolveAPITokens(cfg)
	if err != nil {
		t.Fatalf("ResolveAPITokens after seed: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Token != "boot-token-xyz" {
		t.Fatalf("seeded API token did not resolve; got %+v", resolved)
	}
}

// TestOfflineSeedPersistsAndReloads proves the offline write round-trips through
// the encrypted blob: a fresh Load (a stand-in for the daemon's next boot) sees
// the seeded value and its grant.
func TestOfflineSeedPersistsAndReloads(t *testing.T) {
	_, vaultPath, keyPath := writeSeedConfig(t)

	if err := doVaultSetOffline(vaultPath, keyPath, "webhook-secret", "shhh", []string{"core"}, ""); err != nil {
		t.Fatalf("offline seed: %v", err)
	}

	kr, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	v, err := vault.Load(vaultPath, kr)
	if err != nil {
		t.Fatalf("reload vault: %v", err)
	}
	sec, ok := v.Store().Secret("webhook-secret")
	if !ok {
		t.Fatal("seeded secret missing after reload")
	}
	if sec.Value != "shhh" {
		t.Fatalf("seeded value = %q, want %q", sec.Value, "shhh")
	}
}

// runVaultSetCapturing drives runVaultSet with a controlled stdin (the secret
// value, since it is read from stdin) and captures stderr via the shared
// captureOutputWithExitCode helper. Returns (rc, stderr).
func runVaultSetCapturing(t *testing.T, stdin string, args []string) (int, string) {
	t.Helper()
	oldIn := os.Stdin
	inR, inW, _ := os.Pipe()
	os.Stdin = inR
	go func() { _, _ = inW.WriteString(stdin); _ = inW.Close() }()
	rc, _, stderr := captureOutputWithExitCode(t, func() int { return runVaultSet(args) })
	os.Stdin = oldIn
	return rc, stderr
}

// TestRunVaultSet_RejectsAmbiguousMode — supplying both the offline (--vault/--key)
// and daemon (--api-url) selectors is a clear error, not a silent pick.
func TestRunVaultSet_RejectsAmbiguousMode(t *testing.T) {
	rc, stderr := runVaultSetCapturing(t, "v", []string{
		"--api-url", "http://127.0.0.1:8080", "--vault", "/x/vault.age", "--key", "/x/age.key", "--name", "s",
	})
	if rc == 0 {
		t.Fatal("expected non-zero exit for ambiguous offline+daemon flags")
	}
	if !strings.Contains(stderr, "one mode") {
		t.Fatalf("error should name the mode conflict, got: %q", stderr)
	}
}

// TestRunVaultSet_RejectsIncompleteOffline — offline intent (--vault) without --key
// is refused with the assumption named (daemon must be stopped, need both).
func TestRunVaultSet_RejectsIncompleteOffline(t *testing.T) {
	rc, stderr := runVaultSetCapturing(t, "v", []string{"--vault", "/x/vault.age", "--name", "s"})
	if rc == 0 {
		t.Fatal("expected non-zero exit for --vault without --key")
	}
	if !strings.Contains(stderr, "BOTH --vault and --key") {
		t.Fatalf("error should require both flags, got: %q", stderr)
	}
}

// TestRunVaultSet_OfflineHappyPath — the full CLI offline seed sets the secret in
// the blob (value from stdin) and reports a daemon-stopped local write.
func TestRunVaultSet_OfflineHappyPath(t *testing.T) {
	_, vaultPath, keyPath := writeSeedConfig(t)
	rc, stderr := runVaultSetCapturing(t, "boot-token-xyz", []string{
		"--vault", vaultPath, "--key", keyPath, "--name", "ductile-api-admin", "--principal", "core",
	})
	if rc != 0 {
		t.Fatalf("offline set rc=%d stderr=%q", rc, stderr)
	}
	if !strings.Contains(stderr, "offline") {
		t.Fatalf("success message should name the offline write, got: %q", stderr)
	}
	kr, _ := secrets.LoadKeyringFromFile(keyPath)
	v, err := vault.Load(vaultPath, kr)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if sec, ok := v.Store().Secret("ductile-api-admin"); !ok || sec.Value != "boot-token-xyz" {
		t.Fatalf("offline CLI set did not persist; ok=%v sec=%+v", ok, sec)
	}
}
