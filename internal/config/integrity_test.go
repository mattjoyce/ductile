package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyIntegrityAllValid(t *testing.T) {
	tmpDir := t.TempDir()
	setupIntegrityDir(t, tmpDir)

	files, err := DiscoverConfigFiles(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Generate checksums for all discovered files
	if err := GenerateChecksumsFromDiscovery(files, false); err != nil {
		t.Fatal(err)
	}

	result, err := VerifyIntegrity(tmpDir, files)
	if err != nil {
		t.Fatal(err)
	}

	if !result.Passed {
		t.Errorf("expected Passed=true, got errors: %v", result.Errors)
	}
	if len(result.Warnings) > 0 {
		t.Errorf("unexpected warnings: %v", result.Warnings)
	}
}

// TestChecksumGenerationExcludesOwnDestination guards C-FRO-14.
// Verification note: the claimed impact (integrity generation hashes the
// whole tree including its own .checksums output, so the gate can never
// match itself and trains operators to ignore it) was NOT reproduced.
// Generation hashes an explicit curated file list (ConfigFiles.AllFiles),
// not a directory walk, so .checksums is never swept into its own
// manifest, and a regenerate->verify cycle is stable. This test pins that
// the destination is excluded so a future switch to a directory walk
// cannot silently reintroduce the self-inclusion instability.
func TestChecksumGenerationExcludesOwnDestination(t *testing.T) {
	tmpDir := t.TempDir()
	setupIntegrityDir(t, tmpDir)

	files, err := DiscoverConfigFiles(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := GenerateChecksumsFromDiscovery(files, false); err != nil {
		t.Fatal(err)
	}

	manifest, err := LoadChecksums(tmpDir)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	for path := range manifest.Hashes {
		if filepath.Base(path) == ".checksums" {
			t.Fatalf(".checksums manifest contains its own destination %q (self-inclusion)", path)
		}
	}

	// Regenerate then verify: must stay stable (no self-inclusion drift).
	if err := GenerateChecksumsFromDiscovery(files, false); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	result, err := VerifyIntegrity(tmpDir, files)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed || len(result.Warnings) > 0 {
		t.Fatalf("verify unstable after regenerate: passed=%v errors=%v warnings=%v",
			result.Passed, result.Errors, result.Warnings)
	}
}

func TestVerifyIntegrityHighSecurityMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	setupIntegrityDir(t, tmpDir)

	files, err := DiscoverConfigFiles(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Generate checksums
	if err := GenerateChecksumsFromDiscovery(files, false); err != nil {
		t.Fatal(err)
	}

	// Tamper with webhooks.yaml (high security)
	writeTestFile(t, filepath.Join(tmpDir, "webhooks.yaml"), "webhooks:\n  listen: \"127.0.0.1:9999\"\n")

	result, err := VerifyIntegrity(tmpDir, files)
	if err != nil {
		t.Fatal(err)
	}

	if result.Passed {
		t.Fatal("expected Passed=false for high-security file mismatch")
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected errors for high-security file mismatch")
	}
	if !strings.Contains(result.Errors[0], "hash mismatch") {
		t.Errorf("error should mention hash mismatch, got: %s", result.Errors[0])
	}
}

func TestVerifyIntegrityOperationalMismatchWarns(t *testing.T) {
	tmpDir := t.TempDir()
	setupIntegrityDir(t, tmpDir)

	files, err := DiscoverConfigFiles(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Generate checksums
	if err := GenerateChecksumsFromDiscovery(files, false); err != nil {
		t.Fatal(err)
	}

	// Tamper with a routes file (operational)
	writeTestFile(t, filepath.Join(tmpDir, "routes.yaml"), "routes:\n  - name: tampered\n")

	result, err := VerifyIntegrity(tmpDir, files)
	if err != nil {
		t.Fatal(err)
	}

	if !result.Passed {
		t.Fatal("operational mismatch should not cause hard fail")
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected warnings for operational file mismatch")
	}
}

func TestVerifyIntegrityNoManifestWithHighSecurity(t *testing.T) {
	tmpDir := t.TempDir()
	setupIntegrityDir(t, tmpDir)

	files, err := DiscoverConfigFiles(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Don't generate checksums — should hard fail because tokens.yaml exists
	result, err := VerifyIntegrity(tmpDir, files)
	if err != nil {
		t.Fatal(err)
	}

	if result.Passed {
		t.Fatal("expected Passed=false when no manifest + high-security files")
	}
}

func TestVerifyIntegrityNoManifestNoHighSecurity(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestFile(t, filepath.Join(tmpDir, "config.yaml"), "service:\n  name: test\n")
	writeTestFile(t, filepath.Join(tmpDir, "routes.yaml"), "routes:\n  - name: sample\n")

	files, err := DiscoverConfigFiles(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	result, err := VerifyIntegrity(tmpDir, files)
	if err != nil {
		t.Fatal(err)
	}

	if !result.Passed {
		t.Fatal("no manifest + no high-security files should pass with warning")
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected warning about missing manifest")
	}
}

func setupIntegrityDir(t *testing.T, dir string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, "config.yaml"), "service:\n  name: test\n  tick_interval: 60s\nstate:\n  path: ./test.db\n")
	writeTestFile(t, filepath.Join(dir, "webhooks.yaml"), "webhooks:\n  listen: \"127.0.0.1:8091\"\n")
	writeTestFile(t, filepath.Join(dir, "routes.yaml"), "routes:\n  - name: sample\n")
}
