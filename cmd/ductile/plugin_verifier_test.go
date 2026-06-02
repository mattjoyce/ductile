package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/vault"
)

// buildRegistryAndVault discovers the fixture's plugins into a registry and loads
// its vault — the two runtime inputs the compose-time verifier wraps.
func buildRegistryAndVault(t *testing.T, configDir string) (*plugin.Registry, *vault.Vault) {
	t.Helper()
	configPath := filepath.Join(configDir, "config.yaml")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	roots, err := resolvePluginRoots(cfg, configPath)
	if err != nil {
		t.Fatalf("resolvePluginRoots: %v", err)
	}
	registry, err := plugin.DiscoverManyWithOptions(roots, func(string, string, ...any) {},
		plugin.DiscoverOptions{AllowSymlinks: cfg.Service.AllowSymlinks})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if _, err := plugin.ApplyAliases(registry, cfg.Plugins); err != nil {
		t.Fatalf("aliases: %v", err)
	}
	v, err := config.LoadVault(configDir, cfg)
	if err != nil {
		t.Fatalf("LoadVault: %v", err)
	}
	if v == nil {
		t.Fatal("fixture vault did not load")
	}
	return registry, v
}

// errNonceSource is a nonce source that always fails — proves the adapter denies
// when the vault cannot yield a nonce (no unkeyed downgrade).
type errNonceSource struct{}

func (errNonceSource) FingerprintNonce() ([]byte, error) { return nil, errors.New("no nonce") }

func TestPluginIdentityVerifierCleanPasses(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	reg, v := buildRegistryAndVault(t, tmp)

	pv := newPluginIdentityVerifier(reg, tmp, v)
	if err := pv.VerifyIdentity("gmail"); err != nil {
		t.Fatalf("attested, unchanged plugin must verify: %v", err)
	}
}

func TestPluginIdentityVerifierTamperDenies(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	reg, v := buildRegistryAndVault(t, tmp)

	// Swap the binary after attestation — the runtime-swap §3.3 closes.
	if err := os.WriteFile(filepath.Join(tmp, "plugins", "gmail", "gmail"), []byte("#!/bin/sh\necho swapped\n"), 0o755); err != nil {
		t.Fatalf("swap: %v", err)
	}
	pv := newPluginIdentityVerifier(reg, tmp, v)
	err := pv.VerifyIdentity("gmail")
	if err == nil {
		t.Fatal("a swapped binary must be denied at compose time")
	}
	if !strings.Contains(err.Error(), "entrypoint") {
		t.Fatalf("error should name the entrypoint mismatch: %v", err)
	}
}

func TestPluginIdentityVerifierNoRecordedFingerprintDenies(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	// config lock only — no `plugin lock`, so gmail has no recorded fingerprint.
	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp}); code != 0 {
		t.Fatalf("config lock failed: %s", stderr)
	}
	reg, v := buildRegistryAndVault(t, tmp)

	pv := newPluginIdentityVerifier(reg, tmp, v)
	err := pv.VerifyIdentity("gmail")
	if err == nil {
		t.Fatal("a principal with no recorded fingerprint must be denied (fail closed)")
	}
	if !strings.Contains(err.Error(), "no recorded fingerprint") {
		t.Fatalf("error should explain the missing attestation: %v", err)
	}
}

func TestPluginIdentityVerifierUndiscoverableDenies(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	if err := os.RemoveAll(filepath.Join(tmp, "plugins", "gmail")); err != nil {
		t.Fatalf("remove plugin: %v", err)
	}
	reg, v := buildRegistryAndVault(t, tmp)

	pv := newPluginIdentityVerifier(reg, tmp, v)
	if err := pv.VerifyIdentity("gmail"); err == nil {
		t.Fatal("an undiscoverable plugin must be denied")
	}
}

func TestPluginIdentityVerifierNonceFailureDenies(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	reg, _ := buildRegistryAndVault(t, tmp)

	pv := newPluginIdentityVerifier(reg, tmp, errNonceSource{})
	if err := pv.VerifyIdentity("gmail"); err == nil {
		t.Fatal("a nonce-source failure must deny (no unkeyed downgrade)")
	}
}
