package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fp(name, mhash, ehash string) PluginFingerprint {
	return PluginFingerprint{
		Name:           name,
		Enabled:        true,
		ManifestPath:   "/abs/" + name + "/manifest.yaml",
		ManifestHash:   mhash,
		EntrypointPath: "/abs/" + name + "/" + name,
		EntrypointHash: ehash,
	}
}

// PreservePluginFingerprints carries forward recorded fingerprints for plugins
// that are still configured and drops entries for de-configured plugins. This is
// what a routine `config lock` does instead of re-blessing bytes from disk.
func TestPreservePluginFingerprintsPrunesDeconfigured(t *testing.T) {
	existing := []PluginFingerprint{
		fp("gmail", "m1", "e1"),
		fp("withings", "m2", "e2"),
		fp("ghost", "m3", "e3"), // no longer configured
	}
	configured := map[string]bool{"gmail": true, "withings": false}

	got := PreservePluginFingerprints(existing, configured)
	if len(got) != 2 {
		t.Fatalf("want 2 preserved (gmail, withings), got %d: %+v", len(got), got)
	}
	// Sorted by name.
	if got[0].Name != "gmail" || got[1].Name != "withings" {
		t.Fatalf("preserved set not sorted by name: %+v", got)
	}
	for _, f := range got {
		if f.Name == "ghost" {
			t.Fatalf("de-configured plugin %q must be pruned", f.Name)
		}
	}
	// Bytes carried forward verbatim — never recomputed.
	if got[0].ManifestHash != "m1" || got[0].EntrypointHash != "e1" {
		t.Fatalf("preserved hashes must be carried forward verbatim: %+v", got[0])
	}
}

func TestPreservePluginFingerprintsEmptyInputs(t *testing.T) {
	if got := PreservePluginFingerprints(nil, map[string]bool{"x": true}); len(got) != 0 {
		t.Fatalf("nil existing → empty, got %+v", got)
	}
	if got := PreservePluginFingerprints([]PluginFingerprint{fp("a", "m", "e")}, nil); len(got) != 0 {
		t.Fatalf("nil configured → everything pruned, got %+v", got)
	}
}

// MergePluginFingerprint replaces-or-adds one entry by Name, leaving every other
// entry untouched (ISC-A3: a single-plugin lock never sweeps in plugin B).
func TestMergePluginFingerprintReplacesOnlyNamed(t *testing.T) {
	existing := []PluginFingerprint{fp("a", "ma", "ea"), fp("b", "mb", "eb")}
	got := MergePluginFingerprint(existing, fp("a", "ma2", "ea2"))
	if len(got) != 2 {
		t.Fatalf("merge of existing name must not grow set: %+v", got)
	}
	byName := map[string]PluginFingerprint{}
	for _, f := range got {
		byName[f.Name] = f
	}
	if byName["a"].ManifestHash != "ma2" {
		t.Fatalf("named entry not updated: %+v", byName["a"])
	}
	if byName["b"].ManifestHash != "mb" || byName["b"].EntrypointHash != "eb" {
		t.Fatalf("other entry must be untouched: %+v", byName["b"])
	}
}

func TestMergePluginFingerprintAddsNewSorted(t *testing.T) {
	existing := []PluginFingerprint{fp("b", "mb", "eb")}
	got := MergePluginFingerprint(existing, fp("a", "ma", "ea"))
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("new entry must be added and set sorted by name: %+v", got)
	}
}

// PluginFingerprintsCode is the 5-char alphanumeric confirmation code for
// `plugin lock --all`: deterministic over the proposed set, so it self-
// invalidates the moment any plugin's bytes change (TOCTOU guard).
func TestPluginFingerprintsCodeDeterministicAndBound(t *testing.T) {
	setA := []PluginFingerprint{fp("a", "ma", "ea"), fp("b", "mb", "eb")}
	c1 := PluginFingerprintsCode(setA)
	c2 := PluginFingerprintsCode([]PluginFingerprint{fp("b", "mb", "eb"), fp("a", "ma", "ea")})
	if c1 != c2 {
		t.Fatalf("code must be order-independent (sorted): %q vs %q", c1, c2)
	}
	if len(c1) != 5 {
		t.Fatalf("code must be 5 chars, got %q", c1)
	}
	for _, r := range c1 {
		isAlnum := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !isAlnum {
			t.Fatalf("code must be alphanumeric, got %q", c1)
		}
	}
	// Any byte change → different code.
	setB := []PluginFingerprint{fp("a", "ma", "ea2"), fp("b", "mb", "eb")}
	if PluginFingerprintsCode(setB) == c1 {
		t.Fatalf("code must change when a fingerprint changes")
	}
}

// DiffPluginFingerprints classifies the move from recorded → proposed for the
// --all preview.
func TestDiffPluginFingerprints(t *testing.T) {
	existing := []PluginFingerprint{fp("keep", "m", "e"), fp("change", "m1", "e1"), fp("drop", "m", "e")}
	proposed := []PluginFingerprint{fp("keep", "m", "e"), fp("change", "m2", "e2"), fp("add", "m", "e")}
	changed, added, removed := DiffPluginFingerprints(existing, proposed)
	if strings.Join(changed, ",") != "change" {
		t.Fatalf("changed = %v, want [change]", changed)
	}
	if strings.Join(added, ",") != "add" {
		t.Fatalf("added = %v, want [add]", added)
	}
	if strings.Join(removed, ",") != "drop" {
		t.Fatalf("removed = %v, want [drop]", removed)
	}
}

// GenerateChecksumsWithFingerprints writes config-file hashes plus a precomputed
// fingerprint set, hashing NO plugin bytes itself (the decoupled writer).
func TestGenerateChecksumsWithFingerprintsWritesPreserved(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "config.yaml"), "plugin_roots: [plugins]\nplugins: {}\n")
	files, err := DiscoverConfigFiles(dir)
	if err != nil {
		t.Fatalf("DiscoverConfigFiles: %v", err)
	}
	preserved := []PluginFingerprint{fp("gmail", "m1", "e1")}
	if err := GenerateChecksumsWithFingerprints(files, preserved, false); err != nil {
		t.Fatalf("GenerateChecksumsWithFingerprints: %v", err)
	}
	m, err := LoadChecksums(dir)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if len(m.PluginFingerprints) != 1 || m.PluginFingerprints[0].ManifestHash != "m1" {
		t.Fatalf("fingerprints not written verbatim: %+v", m.PluginFingerprints)
	}
	if len(m.Hashes) == 0 {
		t.Fatalf("config-file hashes must still be written: %+v", m.Hashes)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
