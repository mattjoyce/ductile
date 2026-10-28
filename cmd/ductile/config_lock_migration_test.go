package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
	"gopkg.in/yaml.v3"
)

// A version 2 (BLAKE3) .checksums is refused at load, and one `config lock`
// migrates it: config files and every recorded plugin are re-hashed under
// SHA-256, the clearpass window is dropped, and the result is a version 3
// manifest that `config check` accepts.
func TestConfigLockMigratesLegacyChecksums(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)

	legacyHex := strings.Repeat("a", 64)
	legacy := config.ChecksumManifest{
		Version:     2,
		GeneratedAt: "2026-10-28T00:00:00Z",
		Hashes:      map[string]string{filepath.Join(tmp, "config.yaml"): legacyHex},
		PluginFingerprints: []config.PluginFingerprint{
			{
				Name:           "gmail",
				Enabled:        true,
				ManifestPath:   filepath.Join(tmp, "plugins", "gmail", "manifest.yaml"),
				ManifestHash:   legacyHex,
				EntrypointPath: filepath.Join(tmp, "plugins", "gmail", "gmail"),
				EntrypointHash: legacyHex,
				Clearpass: &config.Clearpass{
					ID:       "0123456789abcdef",
					OpenedAt: "2026-10-28T00:00:00Z",
					Until:    "2026-10-28T01:00:00Z",
					Note:     "legacy window",
					Seal:     legacyHex,
				},
			},
		},
	}
	data, err := yaml.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy checksums: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".checksums"), data, 0o600); err != nil {
		t.Fatalf("write legacy checksums: %v", err)
	}

	if _, err := config.LoadChecksums(tmp); err == nil {
		t.Fatal("a version 2 manifest must be refused before migration")
	}

	code, stdout, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp})
	if code != 0 {
		t.Fatalf("config lock migration failed: code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "Migrating .checksums in "+tmp+" from BLAKE3 (version 2) to SHA-256 (version 3).") {
		t.Fatalf("migration header missing from stdout: %s", stdout)
	}
	if !strings.Contains(stdout, "  REHASH [plugin] gmail (BLAKE3 -> SHA-256)") {
		t.Fatalf("REHASH line missing from stdout: %s", stdout)
	}

	migrated, err := config.LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums after migration: %v", err)
	}
	if migrated.Version != 3 {
		t.Fatalf("version = %d, want 3", migrated.Version)
	}
	if len(migrated.Hashes) == 0 {
		t.Fatal("migrated manifest has no config-file hashes")
	}
	for path, hash := range migrated.Hashes {
		if !strings.HasPrefix(hash, "sha256:") {
			t.Fatalf("config hash for %s not tagged sha256: %s", path, hash)
		}
	}
	if len(migrated.PluginFingerprints) != 1 {
		t.Fatalf("plugin fingerprints = %+v, want exactly gmail", migrated.PluginFingerprints)
	}
	fp := migrated.PluginFingerprints[0]
	if !strings.HasPrefix(fp.ManifestHash, "sha256:") || !strings.HasPrefix(fp.EntrypointHash, "sha256:") {
		t.Fatalf("plugin hashes not tagged sha256: %+v", fp)
	}
	if fp.Clearpass != nil {
		t.Fatalf("migration must drop clearpass records: %+v", fp.Clearpass)
	}

	code, checkOut, checkErr := captureOutputWithExitCode(t, func() int {
		return runConfigCheck([]string{"--config-dir", tmp})
	})
	if code != 0 {
		t.Fatalf("config check after migration failed: code=%d stdout=%s stderr=%s", code, checkOut, checkErr)
	}
}
