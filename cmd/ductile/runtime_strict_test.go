package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
)

func writeStrictTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// setupStrictReloadFixture writes a minimal locked config dir (config.yaml + tokens.yaml + routes.yaml)
// with a valid .checksums manifest. The caller can subsequently tamper with files to simulate drift.
func setupStrictReloadFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeStrictTestFile(t, filepath.Join(dir, "config.yaml"),
		"service:\n  name: test\n  tick_interval: 60s\n"+
			"state:\n  path: ./test.db\n"+
			"plugin_roots:\n  - ./plugins\n")
	writeStrictTestFile(t, filepath.Join(dir, "tokens.yaml"),
		"tokens:\n  - name: admin\n    key: secret123\n")
	writeStrictTestFile(t, filepath.Join(dir, "routes.yaml"),
		"routes:\n  - name: sample\n")
	files, err := config.DiscoverConfigFiles(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := config.GenerateChecksumsFromDiscovery(files, false); err != nil {
		t.Fatalf("generate checksums: %v", err)
	}
	return dir
}

// TestVerifyReloadIntegrityStrictRejectsOperationalMismatch — P2-10 strict-mode reload semantics.
// When strict_mode is true, unlocked operational drift (config.yaml / routes.yaml) must fail reload.
func TestVerifyReloadIntegrityStrictRejectsOperationalMismatch(t *testing.T) {
	dir := setupStrictReloadFixture(t)
	configPath := filepath.Join(dir, "config.yaml")

	// Tamper with an operational file (routes.yaml is TierOperational).
	writeStrictTestFile(t, filepath.Join(dir, "routes.yaml"),
		"routes:\n  - name: tampered\n")

	if err := verifyReloadIntegrity(configPath, true); err == nil {
		t.Fatal("expected strict-mode reload to reject operational mismatch, got nil")
	} else if !strings.Contains(err.Error(), "strict") {
		t.Errorf("error should mention strict mode, got: %v", err)
	}
}

// TestVerifyReloadIntegrityPermissivePassesOperationalMismatch — preserves baseline.
// When strict_mode is false, operational warnings must not block reload (only high-security or
// plugin fingerprint mismatches do).
func TestVerifyReloadIntegrityPermissivePassesOperationalMismatch(t *testing.T) {
	dir := setupStrictReloadFixture(t)
	configPath := filepath.Join(dir, "config.yaml")

	writeStrictTestFile(t, filepath.Join(dir, "routes.yaml"),
		"routes:\n  - name: tampered\n")

	if err := verifyReloadIntegrity(configPath, false); err != nil {
		t.Fatalf("expected permissive-mode reload to pass operational mismatch, got: %v", err)
	}
}

// TestVerifyReloadIntegrityStrictAcceptsCleanReload — strict mode passes when nothing has drifted.
func TestVerifyReloadIntegrityStrictAcceptsCleanReload(t *testing.T) {
	dir := setupStrictReloadFixture(t)
	configPath := filepath.Join(dir, "config.yaml")

	if err := verifyReloadIntegrity(configPath, true); err != nil {
		t.Fatalf("expected clean strict reload to pass, got: %v", err)
	}
}

// TestVerifyReloadIntegrityHighSecMismatchAlwaysRejects — invariant across strict/permissive.
func TestVerifyReloadIntegrityHighSecMismatchAlwaysRejects(t *testing.T) {
	dir := setupStrictReloadFixture(t)
	configPath := filepath.Join(dir, "config.yaml")

	// Tamper with tokens.yaml (high-security).
	writeStrictTestFile(t, filepath.Join(dir, "tokens.yaml"),
		"tokens:\n  - name: tampered\n    key: hacked\n")

	if err := verifyReloadIntegrity(configPath, false); err == nil {
		t.Fatal("expected high-security mismatch to reject reload even in permissive mode")
	}
	if err := verifyReloadIntegrity(configPath, true); err == nil {
		t.Fatal("expected high-security mismatch to reject reload in strict mode")
	}
}
