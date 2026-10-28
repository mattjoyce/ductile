package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGenerateChecksumsWithReportDryRun(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "tokens.yaml"), []byte("tokens:\n  - name: test\n    key: test\n    scopes_file: scopes/test.json\n    scopes_hash: blake3:deadbeef\n"), 0600); err != nil {
		t.Fatal(err)
	}

	report, err := GenerateChecksumsWithReport(tmpDir, []string{"tokens.yaml", "webhooks.yaml"}, true)
	if err != nil {
		t.Fatalf("GenerateChecksumsWithReport() failed: %v", err)
	}

	if report.Written {
		t.Fatal("report.Written = true, want false in dry-run")
	}

	if len(report.Files) != 2 {
		t.Fatalf("len(report.Files) = %d, want 2", len(report.Files))
	}

	if !report.Files[0].Exists || report.Files[0].Hash == "" {
		t.Fatal("tokens.yaml should exist with computed hash")
	}
	if !filepath.IsAbs(report.Files[0].Path) {
		t.Fatal("expected absolute path for tokens.yaml in report")
	}
	if report.Files[1].Exists || report.Files[1].Hash != "" {
		t.Fatal("webhooks.yaml should be reported as missing without hash")
	}
	if !filepath.IsAbs(report.Files[1].Path) {
		t.Fatal("expected absolute path for webhooks.yaml in report")
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".checksums")); !os.IsNotExist(err) {
		t.Fatal(".checksums should not be written in dry-run mode")
	}
}

func TestGenerateChecksumsWithReportWritesChecksums(t *testing.T) {
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "tokens.yaml"), []byte("tokens:\n  - name: test\n    key: test\n    scopes_file: scopes/test.json\n    scopes_hash: blake3:deadbeef\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "webhooks.yaml"), []byte("listen: :8080\n"), 0600); err != nil {
		t.Fatal(err)
	}

	report, err := GenerateChecksumsWithReport(tmpDir, []string{"tokens.yaml", "webhooks.yaml"}, false)
	if err != nil {
		t.Fatalf("GenerateChecksumsWithReport() failed: %v", err)
	}

	if !report.Written {
		t.Fatal("report.Written = false, want true")
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".checksums")); err != nil {
		t.Fatalf("expected .checksums to be written: %v", err)
	}

	manifest, err := LoadChecksums(tmpDir)
	if err != nil {
		t.Fatalf("LoadChecksums() failed: %v", err)
	}
	if len(manifest.Hashes) != 2 {
		t.Fatalf("len(manifest.Hashes) = %d, want 2", len(manifest.Hashes))
	}
}

// TestWriteChecksumsAtomicNoTempLeftover verifies that a successful write leaves
// no .checksums.tmp-* artifacts in the config directory.
func TestWriteChecksumsAtomicNoTempLeftover(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".checksums")
	manifest := ChecksumManifest{
		Version:     2,
		GeneratedAt: "2026-04-18T00:00:00Z",
		Hashes:      map[string]string{"/a": "abc"},
	}

	if err := writeChecksumsAtomic(path, manifest); err != nil {
		t.Fatalf("writeChecksumsAtomic() failed: %v", err)
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == ".checksums" {
			continue
		}
		t.Fatalf("unexpected leftover in config dir after atomic write: %s", name)
	}
}

// TestChecksumManifestRoundTripWithoutFingerprints ensures an empty slice of
// PluginFingerprints is omitted from the YAML (backward-compat with deployments
// locked before this field existed).
func TestChecksumManifestRoundTripWithoutFingerprints(t *testing.T) {
	original := ChecksumManifest{
		Version:     2,
		GeneratedAt: "2026-04-18T00:00:00Z",
		Hashes:      map[string]string{"/a": "abc"},
	}
	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := "plugin_fingerprints"; containsString(string(data), want) {
		t.Fatalf("marshal unexpectedly included %q: %s", want, string(data))
	}

	var got ChecksumManifest
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != 2 || got.Hashes["/a"] != "abc" {
		t.Fatalf("round trip lost data: %+v", got)
	}
	if len(got.PluginFingerprints) != 0 {
		t.Fatalf("PluginFingerprints should be empty, got %+v", got.PluginFingerprints)
	}
}

// TestChecksumManifestRoundTripWithFingerprints round-trips a manifest that
// carries plugin_fingerprints entries including an alias (Uses set) and a
// disabled configured plugin.
func TestChecksumManifestRoundTripWithFingerprints(t *testing.T) {
	original := ChecksumManifest{
		Version:     2,
		GeneratedAt: "2026-04-18T00:00:00Z",
		Hashes:      map[string]string{"/config.yaml": "abc"},
		PluginFingerprints: []PluginFingerprint{
			{
				Name:           "gmail",
				Enabled:        true,
				ManifestPath:   "/p/gmail/manifest.yaml",
				ManifestHash:   "man-hash-1",
				EntrypointPath: "/p/gmail/gmail",
				EntrypointHash: "bin-hash-1",
			},
			{
				Name:           "gmail-work",
				Enabled:        true,
				Uses:           "gmail",
				ManifestPath:   "/p/gmail/manifest.yaml",
				ManifestHash:   "man-hash-1",
				EntrypointPath: "/p/gmail/gmail",
				EntrypointHash: "bin-hash-1",
			},
			{
				Name:           "legacy",
				Enabled:        false,
				ManifestPath:   "/p/legacy/manifest.yaml",
				ManifestHash:   "man-hash-2",
				EntrypointPath: "/p/legacy/run",
				EntrypointHash: "bin-hash-2",
			},
		},
	}
	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ChecksumManifest
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.PluginFingerprints) != 3 {
		t.Fatalf("expected 3 fingerprints, got %d", len(got.PluginFingerprints))
	}
	alias := got.PluginFingerprints[1]
	if alias.Name != "gmail-work" || alias.Uses != "gmail" {
		t.Fatalf("alias not preserved: %+v", alias)
	}
	legacy := got.PluginFingerprints[2]
	if legacy.Enabled {
		t.Fatal("disabled fingerprint Enabled flag not preserved")
	}
}

// containsString is a local helper to avoid pulling strings import just for this.
func containsString(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestWriteChecksumsAtomicOverwritesCleanly verifies that a second atomic write
// replaces the previous .checksums without leaving a temp file and that the
// final contents match the second write.
func TestWriteChecksumsAtomicOverwritesCleanly(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".checksums")

	first := ChecksumManifest{Version: 3, GeneratedAt: "first", Hashes: map[string]string{"/a": "111"}}
	if err := writeChecksumsAtomic(path, first); err != nil {
		t.Fatalf("first write failed: %v", err)
	}

	second := ChecksumManifest{Version: 3, GeneratedAt: "second", Hashes: map[string]string{"/a": "222"}}
	if err := writeChecksumsAtomic(path, second); err != nil {
		t.Fatalf("second write failed: %v", err)
	}

	got, err := LoadChecksums(tmpDir)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if got.GeneratedAt != "second" || got.Hashes["/a"] != "222" {
		t.Fatalf("manifest did not reflect second write: %+v", got)
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != ".checksums" {
			t.Fatalf("unexpected leftover after overwrite: %s", e.Name())
		}
	}
}
