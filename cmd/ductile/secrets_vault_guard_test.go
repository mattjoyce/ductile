package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestSamePath covers the path-equality core of the #31 guard: clean-equivalent
// and symlinked paths match; distinct paths don't.
func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "v.age")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !samePath(f, f) {
		t.Error("identical paths should match")
	}
	if !samePath(f, filepath.Join(dir, ".", "v.age")) {
		t.Error("clean-equivalent paths should match")
	}
	if samePath(f, filepath.Join(dir, "other.age")) {
		t.Error("different paths should not match")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err == nil {
		if !samePath(f, filepath.Join(link, "v.age")) {
			t.Error("symlinked path should resolve to the same file")
		}
	}
}

// TestVaultGuardPathResolves: a ductile config dir resolves its vault blob path;
// a non-config dir resolves nothing (generic mode — guard stays off).
func TestVaultGuardPathResolves(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	if vp := vaultGuardPath(tmp); vp == "" {
		t.Fatal("expected a resolvable vault path for a ductile config dir")
	}
	if got := vaultGuardPath(filepath.Join(t.TempDir(), "not-a-config")); got != "" {
		t.Errorf("non-config dir should not resolve a vault path, got %q", got)
	}
}

// TestSecretsRotateRefusesVaultPath: secrets rotate must refuse to rewrite the
// configured vault blob and leave it byte-for-byte unchanged (#31).
func TestSecretsRotateRefusesVaultPath(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	vp := vaultGuardPath(tmp)
	if vp == "" {
		t.Fatal("fixture has no resolvable vault path")
	}
	before, err := os.ReadFile(vp)
	if err != nil {
		t.Fatalf("read vault: %v", err)
	}
	code := runSecretsRotate([]string{
		"--key", filepath.Join(tmp, "age.key"),
		"--file", vp,
		"--config", tmp,
		"--recipient", "age1dummy",
	})
	if code != 1 {
		t.Fatalf("guard should refuse rotating the vault, exit=%d", code)
	}
	after, err := os.ReadFile(vp)
	if err != nil {
		t.Fatalf("read vault after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("vault blob was rewritten despite the guard")
	}
}
