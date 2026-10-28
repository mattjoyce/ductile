package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeChecksumManifest(t *testing.T, dir string, manifest ChecksumManifest) {
	t.Helper()
	data, err := yaml.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal checksums: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".checksums"), data, 0o600); err != nil {
		t.Fatalf("write checksums: %v", err)
	}
}

func TestLoadChecksumsVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		version     int
		wantLegacy  bool
		wantErr     bool
		wantVersion int
	}{
		{name: "version 2 is legacy", version: 2, wantLegacy: true, wantErr: true},
		{name: "version 3 loads", version: 3, wantVersion: 3},
		{name: "version 1 is unsupported", version: 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeChecksumManifest(t, dir, ChecksumManifest{
				Version:     tt.version,
				GeneratedAt: "2026-10-28T00:00:00Z",
				Hashes:      map[string]string{"/config.yaml": "abc"},
			})

			got, err := LoadChecksums(dir)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if got := errors.Is(err, ErrChecksumsLegacy); got != tt.wantLegacy {
					t.Fatalf("errors.Is(err, ErrChecksumsLegacy) = %t, want %t (err=%v)", got, tt.wantLegacy, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadChecksums: %v", err)
			}
			if got.Version != tt.wantVersion {
				t.Fatalf("version = %d, want %d", got.Version, tt.wantVersion)
			}
		})
	}
}

func TestLegacyPluginNames(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeChecksumManifest(t, dir, ChecksumManifest{
		Version:     2,
		GeneratedAt: "2026-10-28T00:00:00Z",
		Hashes:      map[string]string{"/config.yaml": "abc"},
		PluginFingerprints: []PluginFingerprint{
			{Name: "zeta", ManifestHash: "m", EntrypointHash: "e"},
			{Name: "alpha", ManifestHash: "m", EntrypointHash: "e"},
			{Name: "mike", ManifestHash: "m", EntrypointHash: "e"},
		},
	})

	got, err := LegacyPluginNames(dir)
	if err != nil {
		t.Fatalf("LegacyPluginNames: %v", err)
	}
	want := []string{"alpha", "mike", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
}

func TestLegacyPluginNamesRejectsVersion3(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeChecksumManifest(t, dir, ChecksumManifest{
		Version:     3,
		GeneratedAt: "2026-10-28T00:00:00Z",
		Hashes:      map[string]string{"/config.yaml": "abc"},
		PluginFingerprints: []PluginFingerprint{
			{Name: "gmail", ManifestHash: "m", EntrypointHash: "e"},
		},
	})

	if _, err := LegacyPluginNames(dir); err == nil {
		t.Fatal("expected an error for a version 3 manifest")
	}
}
