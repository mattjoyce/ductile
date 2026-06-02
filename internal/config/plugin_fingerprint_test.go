package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testFPNonce returns a deterministic 32-byte nonce so keyed-BLAKE3 hashing is
// reproducible across a generate-then-verify pair within a single test.
func testFPNonce() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func writePluginFixture(t *testing.T, dir, name, manifestBody, binaryBody string) (manifestPath, entrypointPath string) {
	t.Helper()
	pluginDir := filepath.Join(dir, name)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", pluginDir, err)
	}
	manifestPath = filepath.Join(pluginDir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifestBody), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	entrypointPath = filepath.Join(pluginDir, name)
	if err := os.WriteFile(entrypointPath, []byte(binaryBody), 0755); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}
	return manifestPath, entrypointPath
}

func TestComputePluginFingerprintHappyPath(t *testing.T) {
	tmp := t.TempDir()
	// Resolve macOS /var → /private/var so the configured path matches
	// what filepath.EvalSymlinks returns inside ComputePluginFingerprint.
	if resolved, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = resolved
	}
	manifest, entry := writePluginFixture(t, tmp, "gmail", "name: gmail\n", "#!/bin/sh\necho hi\n")

	fp, err := ComputePluginFingerprint(ResolvedPlugin{
		Name:           "gmail",
		Enabled:        true,
		ManifestPath:   manifest,
		EntrypointPath: entry,
	}, testFPNonce())
	if err != nil {
		t.Fatalf("ComputePluginFingerprint: %v", err)
	}
	if fp.Name != "gmail" || !fp.Enabled || fp.Uses != "" {
		t.Fatalf("identity fields wrong: %+v", fp)
	}
	if fp.ManifestPath != manifest || fp.EntrypointPath != entry {
		t.Fatalf("paths not preserved: %+v", fp)
	}
	if fp.ManifestResolvedPath == "" || fp.EntrypointResolvedPath == "" {
		t.Fatalf("resolved paths not recorded: %+v", fp)
	}
	if fp.ManifestResolvedPath != manifest || fp.EntrypointResolvedPath != entry {
		t.Fatalf("unexpected resolved paths: %+v", fp)
	}
	if len(fp.ManifestHash) != 64 || len(fp.EntrypointHash) != 64 {
		t.Fatalf("hashes not BLAKE3 hex (len != 64): %+v", fp)
	}
	if fp.ManifestHash == fp.EntrypointHash {
		t.Fatal("manifest and entrypoint should hash differently given different bytes")
	}
}

func TestComputePluginFingerprintAliasCarriesUses(t *testing.T) {
	tmp := t.TempDir()
	manifest, entry := writePluginFixture(t, tmp, "gmail", "name: gmail\n", "bin-bytes\n")

	fp, err := ComputePluginFingerprint(ResolvedPlugin{
		Name:           "gmail-work",
		Enabled:        true,
		Uses:           "gmail",
		ManifestPath:   manifest,
		EntrypointPath: entry,
	}, testFPNonce())
	if err != nil {
		t.Fatalf("ComputePluginFingerprint: %v", err)
	}
	if fp.Name != "gmail-work" || fp.Uses != "gmail" {
		t.Fatalf("alias identity wrong: %+v", fp)
	}
	if fp.ManifestPath != manifest {
		t.Fatalf("alias should reuse base manifest path, got %s", fp.ManifestPath)
	}
}

func TestComputePluginFingerprintDisabledStillHashes(t *testing.T) {
	tmp := t.TempDir()
	manifest, entry := writePluginFixture(t, tmp, "legacy", "name: legacy\n", "legacy-bin\n")

	fp, err := ComputePluginFingerprint(ResolvedPlugin{
		Name:           "legacy",
		Enabled:        false,
		ManifestPath:   manifest,
		EntrypointPath: entry,
	}, testFPNonce())
	if err != nil {
		t.Fatalf("ComputePluginFingerprint: %v", err)
	}
	if fp.Enabled {
		t.Fatal("disabled flag not preserved")
	}
	if fp.ManifestHash == "" || fp.EntrypointHash == "" {
		t.Fatal("disabled plugin should still be hashed")
	}
}

func TestComputePluginFingerprintMissingManifest(t *testing.T) {
	tmp := t.TempDir()
	_, entry := writePluginFixture(t, tmp, "gmail", "name: gmail\n", "bin\n")
	missingManifest := filepath.Join(tmp, "no-such", "manifest.yaml")

	_, err := ComputePluginFingerprint(ResolvedPlugin{
		Name:           "gmail",
		Enabled:        true,
		ManifestPath:   missingManifest,
		EntrypointPath: entry,
	}, testFPNonce())
	if err == nil {
		t.Fatal("expected error for missing manifest, got nil")
	}
	if !strings.Contains(err.Error(), "gmail") || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("error should name plugin and kind: %v", err)
	}
}

func TestComputePluginFingerprintMissingEntrypoint(t *testing.T) {
	tmp := t.TempDir()
	manifest, _ := writePluginFixture(t, tmp, "gmail", "name: gmail\n", "bin\n")
	missingEntry := filepath.Join(tmp, "gmail", "no-such-binary")

	_, err := ComputePluginFingerprint(ResolvedPlugin{
		Name:           "gmail",
		Enabled:        true,
		ManifestPath:   manifest,
		EntrypointPath: missingEntry,
	}, testFPNonce())
	if err == nil {
		t.Fatal("expected error for missing entrypoint, got nil")
	}
	if !strings.Contains(err.Error(), "gmail") || !strings.Contains(err.Error(), "entrypoint") {
		t.Fatalf("error should name plugin and kind: %v", err)
	}
}

func TestComputePluginFingerprintRequiresAbsolutePaths(t *testing.T) {
	_, err := ComputePluginFingerprint(ResolvedPlugin{
		Name:           "gmail",
		Enabled:        true,
		ManifestPath:   "relative/manifest.yaml",
		EntrypointPath: "/abs/entrypoint",
	}, testFPNonce())
	if err == nil {
		t.Fatal("expected error for relative manifest path")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("error should mention absolute: %v", err)
	}

	_, err = ComputePluginFingerprint(ResolvedPlugin{
		Name:           "gmail",
		Enabled:        true,
		ManifestPath:   "/abs/manifest.yaml",
		EntrypointPath: "rel/binary",
	}, testFPNonce())
	if err == nil {
		t.Fatal("expected error for relative entrypoint path")
	}
}

func TestComputePluginFingerprintRequiresName(t *testing.T) {
	_, err := ComputePluginFingerprint(ResolvedPlugin{
		ManifestPath:   "/abs/m.yaml",
		EntrypointPath: "/abs/e",
	}, testFPNonce())
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

// lockFixture builds a minimal ConfigFiles rooted under tmp with one config.yaml
// so GenerateChecksumsWithPlugins has something to hash in addition to plugins.
func lockFixture(t *testing.T) (*ConfigFiles, string) {
	t.Helper()
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("service: {}\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return &ConfigFiles{Root: tmp, Config: configPath}, tmp
}

func TestGenerateChecksumsWithPluginsHappyPath(t *testing.T) {
	files, tmp := lockFixture(t)
	pluginsDir := filepath.Join(tmp, "plugins")
	man, entry := writePluginFixture(t, pluginsDir, "gmail", "name: gmail\n", "bin\n")

	err := GenerateChecksumsWithPlugins(files, []ResolvedPlugin{{
		Name:           "gmail",
		Enabled:        true,
		ManifestPath:   man,
		EntrypointPath: entry,
	}}, testFPNonce(), false)
	if err != nil {
		t.Fatalf("GenerateChecksumsWithPlugins: %v", err)
	}

	m, err := LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if len(m.PluginFingerprints) != 1 {
		t.Fatalf("expected 1 fingerprint, got %d", len(m.PluginFingerprints))
	}
	if m.PluginFingerprints[0].Name != "gmail" {
		t.Fatalf("wrong fingerprint: %+v", m.PluginFingerprints[0])
	}
	if _, ok := m.Hashes[files.Config]; !ok {
		t.Fatal("config.yaml hash missing from manifest")
	}
}

func TestGenerateChecksumsWithPluginsSortedByName(t *testing.T) {
	files, tmp := lockFixture(t)
	pluginsDir := filepath.Join(tmp, "plugins")
	manA, entryA := writePluginFixture(t, pluginsDir, "alpha", "name: alpha\n", "a\n")
	manZ, entryZ := writePluginFixture(t, pluginsDir, "zulu", "name: zulu\n", "z\n")
	manM, entryM := writePluginFixture(t, pluginsDir, "mike", "name: mike\n", "m\n")

	input := []ResolvedPlugin{
		{Name: "zulu", Enabled: true, ManifestPath: manZ, EntrypointPath: entryZ},
		{Name: "alpha", Enabled: true, ManifestPath: manA, EntrypointPath: entryA},
		{Name: "mike", Enabled: true, ManifestPath: manM, EntrypointPath: entryM},
	}
	if err := GenerateChecksumsWithPlugins(files, input, testFPNonce(), false); err != nil {
		t.Fatalf("generate: %v", err)
	}

	m, err := LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := []string{m.PluginFingerprints[0].Name, m.PluginFingerprints[1].Name, m.PluginFingerprints[2].Name}
	want := []string{"alpha", "mike", "zulu"}
	for i, g := range got {
		if g != want[i] {
			t.Fatalf("fingerprint order: got %v want %v", got, want)
		}
	}
}

func TestGenerateChecksumsWithPluginsDisabledStillRecorded(t *testing.T) {
	files, tmp := lockFixture(t)
	pluginsDir := filepath.Join(tmp, "plugins")
	man, entry := writePluginFixture(t, pluginsDir, "legacy", "name: legacy\n", "l\n")

	err := GenerateChecksumsWithPlugins(files, []ResolvedPlugin{{
		Name:           "legacy",
		Enabled:        false,
		ManifestPath:   man,
		EntrypointPath: entry,
	}}, testFPNonce(), false)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	m, err := LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(m.PluginFingerprints) != 1 {
		t.Fatalf("expected 1 fingerprint, got %d", len(m.PluginFingerprints))
	}
	if m.PluginFingerprints[0].Enabled {
		t.Fatal("disabled plugin flagged as enabled in manifest")
	}
}

func TestGenerateChecksumsWithPluginsAliasEmbedsUses(t *testing.T) {
	files, tmp := lockFixture(t)
	pluginsDir := filepath.Join(tmp, "plugins")
	man, entry := writePluginFixture(t, pluginsDir, "gmail", "name: gmail\n", "b\n")

	err := GenerateChecksumsWithPlugins(files, []ResolvedPlugin{
		{Name: "gmail", Enabled: true, ManifestPath: man, EntrypointPath: entry},
		{Name: "gmail-work", Enabled: true, Uses: "gmail", ManifestPath: man, EntrypointPath: entry},
	}, testFPNonce(), false)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	m, _ := LoadChecksums(tmp)
	// Sorted: gmail, gmail-work
	if m.PluginFingerprints[1].Uses != "gmail" {
		t.Fatalf("alias Uses not preserved: %+v", m.PluginFingerprints[1])
	}
	if m.PluginFingerprints[0].ManifestHash != m.PluginFingerprints[1].ManifestHash {
		t.Fatal("alias and base should share manifest hash (same bytes)")
	}
}

func TestGenerateChecksumsWithPluginsMissingPluginErrors(t *testing.T) {
	files, tmp := lockFixture(t)

	err := GenerateChecksumsWithPlugins(files, []ResolvedPlugin{{
		Name:           "ghost",
		Enabled:        true,
		ManifestPath:   filepath.Join(tmp, "no-such", "manifest.yaml"),
		EntrypointPath: filepath.Join(tmp, "no-such", "ghost"),
	}}, testFPNonce(), false)
	if err == nil {
		t.Fatal("expected hard error when plugin files missing")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error should name the plugin: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tmp, ".checksums")); !os.IsNotExist(statErr) {
		t.Fatal(".checksums must not be written when generator errors out")
	}
}

func TestGenerateChecksumsWithPluginsEmptyOmitsSection(t *testing.T) {
	files, tmp := lockFixture(t)

	err := GenerateChecksumsWithPlugins(files, nil, testFPNonce(), false)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	m, err := LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(m.PluginFingerprints) != 0 {
		t.Fatalf("expected empty fingerprints, got %+v", m.PluginFingerprints)
	}

	// Also assert raw yaml bytes do not contain the key (omitempty)
	// so an older binary without the field still round-trips cleanly.
	data, err := os.ReadFile(filepath.Join(tmp, ".checksums"))
	if err != nil {
		t.Fatalf("read checksums: %v", err)
	}
	if containsString(string(data), "plugin_fingerprints") {
		t.Fatalf("empty plugin list should not emit plugin_fingerprints key: %s", string(data))
	}
}

// lockAndCurrent builds a fingerprint-plus-current pair for verify tests:
// writes a plugin, computes its fingerprint, and returns both the fingerprint
// AND the matching ResolvedPlugin (same paths, same bytes).
func lockAndCurrent(t *testing.T, dir, name string, enabled bool, uses string) (PluginFingerprint, ResolvedPlugin) {
	t.Helper()
	man, entry := writePluginFixture(t, dir, name, "name: "+name+"\n", "binary-"+name+"\n")
	fp, err := ComputePluginFingerprint(ResolvedPlugin{
		Name:           name,
		Enabled:        enabled,
		Uses:           uses,
		ManifestPath:   man,
		EntrypointPath: entry,
	}, testFPNonce())
	if err != nil {
		t.Fatalf("lockAndCurrent fingerprint: %v", err)
	}
	return fp, ResolvedPlugin{
		Name:           name,
		Enabled:        enabled,
		Uses:           uses,
		ManifestPath:   man,
		EntrypointPath: entry,
	}
}

func TestVerifyPluginFingerprintsEmptyIsPassNoOp(t *testing.T) {
	r := VerifyPluginFingerprints(nil, nil, map[string]ResolvedPlugin{}, testFPNonce())
	if r == nil || !r.Passed || len(r.Errors) != 0 || len(r.Warnings) != 0 {
		t.Fatalf("expected clean pass, got %+v", r)
	}
}

// TestVerifyPluginFingerprintsFailsClosedWithoutNonce proves the downgrade
// guard: non-empty recorded fingerprints are keyed, so verification without the
// 32-byte vault nonce must fail closed rather than silently pass unkeyed.
func TestVerifyPluginFingerprintsFailsClosedWithoutNonce(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "gmail", true, "")
	current := map[string]ResolvedPlugin{"gmail": cur}
	configured := configuredPlugins(current)

	// nil nonce: cannot verify keyed fingerprints, must fail closed.
	r := VerifyPluginFingerprints([]PluginFingerprint{fp}, configured, current, nil)
	if r.Passed {
		t.Fatal("verification with nil nonce must fail closed (downgrade guard)")
	}
	if len(r.Errors) == 0 {
		t.Fatalf("fail-closed result should report an error: %+v", r)
	}

	// 16-byte (wrong-length) nonce must also fail closed.
	short := make([]byte, 16)
	r = VerifyPluginFingerprints([]PluginFingerprint{fp}, configured, current, short)
	if r.Passed {
		t.Fatal("verification with a 16-byte nonce must fail closed")
	}
	if len(r.Errors) == 0 {
		t.Fatalf("short-nonce result should report an error: %+v", r)
	}
}

func configuredPlugins(current map[string]ResolvedPlugin) map[string]bool {
	out := make(map[string]bool, len(current))
	for name, plugin := range current {
		out[name] = plugin.Enabled
	}
	return out
}

func verifyPluginFingerprintsForTest(fingerprints []PluginFingerprint, current map[string]ResolvedPlugin) *IntegrityResult {
	return VerifyPluginFingerprints(fingerprints, configuredPlugins(current), current, testFPNonce())
}

func TestVerifyPluginFingerprintsHappyPath(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "gmail", true, "")

	current := map[string]ResolvedPlugin{"gmail": cur}
	r := verifyPluginFingerprintsForTest([]PluginFingerprint{fp}, current)
	if !r.Passed || len(r.Errors) != 0 || len(r.Warnings) != 0 {
		t.Fatalf("expected clean verify, got %+v", r)
	}
}

func TestVerifyPluginFingerprintsEnabledManifestMismatchErrors(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "gmail", true, "")
	// Rewrite manifest bytes post-lock.
	if err := os.WriteFile(cur.ManifestPath, []byte("tampered\n"), 0644); err != nil {
		t.Fatalf("tamper manifest: %v", err)
	}

	current := map[string]ResolvedPlugin{"gmail": cur}
	r := verifyPluginFingerprintsForTest([]PluginFingerprint{fp}, current)
	if r.Passed {
		t.Fatal("expected Passed=false for enabled manifest mismatch")
	}
	if len(r.Errors) != 1 {
		t.Fatalf("want 1 error, got %d: %+v", len(r.Errors), r)
	}
	msg := r.Errors[0]
	for _, want := range []string{"gmail", "manifest", "mismatch", cur.ManifestPath, "ductile config lock"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q: %s", want, msg)
		}
	}
}

func TestVerifyPluginFingerprintsEnabledEntrypointMismatchErrors(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "gmail", true, "")
	if err := os.WriteFile(cur.EntrypointPath, []byte("new-binary\n"), 0755); err != nil {
		t.Fatalf("tamper entrypoint: %v", err)
	}

	current := map[string]ResolvedPlugin{"gmail": cur}
	r := verifyPluginFingerprintsForTest([]PluginFingerprint{fp}, current)
	if r.Passed {
		t.Fatal("expected Passed=false for enabled entrypoint mismatch")
	}
	if len(r.Errors) != 1 {
		t.Fatalf("want 1 error, got %+v", r)
	}
	if !strings.Contains(r.Errors[0], "entrypoint") {
		t.Fatalf("error should mention entrypoint: %s", r.Errors[0])
	}
}

func TestVerifyPluginFingerprintsDisabledMismatchIsWarning(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "legacy", false, "")
	if err := os.WriteFile(cur.EntrypointPath, []byte("rebuilt\n"), 0755); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	current := map[string]ResolvedPlugin{"legacy": cur}
	r := verifyPluginFingerprintsForTest([]PluginFingerprint{fp}, current)
	if !r.Passed {
		t.Fatal("disabled mismatch must not fail verify")
	}
	if len(r.Warnings) == 0 {
		t.Fatal("disabled mismatch should warn")
	}
	if len(r.Errors) != 0 {
		t.Fatalf("no errors expected: %+v", r.Errors)
	}
}

func TestVerifyPluginFingerprintsStaleRecordIsWarning(t *testing.T) {
	tmp := t.TempDir()
	fp, _ := lockAndCurrent(t, tmp, "removed", true, "")
	r := VerifyPluginFingerprints([]PluginFingerprint{fp}, map[string]bool{}, map[string]ResolvedPlugin{}, testFPNonce())
	if !r.Passed {
		t.Fatal("stale entry must not fail verify")
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("want 1 warning, got %+v", r)
	}
	if !strings.Contains(r.Warnings[0], "no longer configured") {
		t.Fatalf("warning should explain staleness: %s", r.Warnings[0])
	}
}

func TestVerifyPluginFingerprintsMissingFileEnabledErrors(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "gmail", true, "")
	if err := os.Remove(cur.EntrypointPath); err != nil {
		t.Fatalf("rm entrypoint: %v", err)
	}

	current := map[string]ResolvedPlugin{"gmail": cur}
	r := verifyPluginFingerprintsForTest([]PluginFingerprint{fp}, current)
	if r.Passed {
		t.Fatal("missing enabled entrypoint must fail verify")
	}
	if len(r.Errors) != 1 {
		t.Fatalf("want 1 error, got %+v", r)
	}
}

func TestVerifyPluginFingerprintsMissingFileDisabledWarns(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "legacy", false, "")
	if err := os.Remove(cur.EntrypointPath); err != nil {
		t.Fatalf("rm: %v", err)
	}

	current := map[string]ResolvedPlugin{"legacy": cur}
	r := verifyPluginFingerprintsForTest([]PluginFingerprint{fp}, current)
	if !r.Passed {
		t.Fatal("missing disabled entrypoint must not fail verify")
	}
	if len(r.Warnings) == 0 {
		t.Fatal("expected a warning")
	}
}

func TestVerifyPluginFingerprintsPathDriftBytesMatchIsInformational(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "gmail", true, "")

	// Move the files to a new path but keep identical bytes.
	newDir := filepath.Join(tmp, "alt")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatal(err)
	}
	manData, _ := os.ReadFile(cur.ManifestPath)
	entryData, _ := os.ReadFile(cur.EntrypointPath)
	newMan := filepath.Join(newDir, "manifest.yaml")
	newEntry := filepath.Join(newDir, "gmail")
	if err := os.WriteFile(newMan, manData, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newEntry, entryData, 0755); err != nil {
		t.Fatal(err)
	}

	currentShifted := ResolvedPlugin{
		Name:           "gmail",
		Enabled:        true,
		ManifestPath:   newMan,
		EntrypointPath: newEntry,
	}
	current := map[string]ResolvedPlugin{"gmail": currentShifted}
	r := verifyPluginFingerprintsForTest([]PluginFingerprint{fp}, current)
	if !r.Passed {
		t.Fatalf("path drift with identical bytes must not fail verify: %+v", r)
	}
	if len(r.Errors) != 0 {
		t.Fatalf("expected no errors, got %+v", r.Errors)
	}
	if len(r.Warnings) != 2 {
		t.Fatalf("expected 2 informational path-drift warnings (manifest + entrypoint), got %+v", r.Warnings)
	}
}

func TestComputePluginFingerprintRecordsConfiguredAndResolvedPaths(t *testing.T) {
	tmp := t.TempDir()
	// Resolve macOS /var → /private/var so the asserted resolved path
	// (which compares against filepath.EvalSymlinks output) matches.
	if resolved, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = resolved
	}
	realDir := filepath.Join(tmp, "real")
	manifest, entry := writePluginFixture(t, realDir, "gmail", "name: gmail\n", "#!/bin/sh\necho hi\n")
	linkDir := filepath.Join(tmp, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	configuredManifest := filepath.Join(linkDir, "gmail", "manifest.yaml")
	configuredEntry := filepath.Join(linkDir, "gmail", "gmail")

	fp, err := ComputePluginFingerprint(ResolvedPlugin{
		Name:           "gmail",
		Enabled:        true,
		ManifestPath:   configuredManifest,
		EntrypointPath: configuredEntry,
	}, testFPNonce())
	if err != nil {
		t.Fatalf("ComputePluginFingerprint: %v", err)
	}
	if fp.ManifestPath != configuredManifest || fp.EntrypointPath != configuredEntry {
		t.Fatalf("configured paths not preserved: %+v", fp)
	}
	if fp.ManifestResolvedPath != manifest || fp.EntrypointResolvedPath != entry {
		t.Fatalf("resolved paths not recorded correctly: %+v", fp)
	}
}

func TestVerifyPluginFingerprintsDisabledAtLockEnabledNowMismatchErrors(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "gmail", false, "")
	cur.Enabled = true
	if err := os.WriteFile(cur.EntrypointPath, []byte("new-binary\n"), 0755); err != nil {
		t.Fatalf("tamper entrypoint: %v", err)
	}

	current := map[string]ResolvedPlugin{"gmail": cur}
	r := verifyPluginFingerprintsForTest([]PluginFingerprint{fp}, current)
	if r.Passed {
		t.Fatal("current enabled state must make mismatch fatal even if lock recorded disabled")
	}
	if len(r.Errors) != 1 {
		t.Fatalf("want 1 error, got %+v", r)
	}
	if len(r.Warnings) == 0 {
		t.Fatal("expected warning about enabled state drift")
	}
}

func TestVerifyPluginFingerprintsConfiguredButNotDiscoveredErrorsWhenEnabled(t *testing.T) {
	tmp := t.TempDir()
	fp, _ := lockAndCurrent(t, tmp, "gmail", true, "")

	r := VerifyPluginFingerprints(
		[]PluginFingerprint{fp},
		map[string]bool{"gmail": true},
		map[string]ResolvedPlugin{},
		testFPNonce(),
	)
	if r.Passed {
		t.Fatal("configured enabled plugin missing from registry must fail verify")
	}
	if len(r.Errors) != 1 || !strings.Contains(r.Errors[0], "configured but was not discovered") {
		t.Fatalf("unexpected errors: %+v", r.Errors)
	}
}

func TestVerifyPluginFingerprintsConfiguredButNotDiscoveredWarnsWhenDisabled(t *testing.T) {
	tmp := t.TempDir()
	fp, _ := lockAndCurrent(t, tmp, "legacy", false, "")

	r := VerifyPluginFingerprints(
		[]PluginFingerprint{fp},
		map[string]bool{"legacy": false},
		map[string]ResolvedPlugin{},
		testFPNonce(),
	)
	if !r.Passed {
		t.Fatalf("configured disabled missing plugin should warn only: %+v", r)
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("want 1 warning, got %+v", r)
	}
}

func TestVerifyPluginFingerprintsConfiguredPluginMissingFromLockErrorsWhenEnabled(t *testing.T) {
	current := map[string]ResolvedPlugin{
		"gmail": {
			Name:    "gmail",
			Enabled: true,
		},
	}

	r := VerifyPluginFingerprints(
		[]PluginFingerprint{{Name: "other", Enabled: true}},
		configuredPlugins(current),
		current,
		testFPNonce(),
	)
	if r.Passed {
		t.Fatal("configured enabled plugin missing from lock must fail verify")
	}
	if len(r.Errors) == 0 || !strings.Contains(r.Errors[len(r.Errors)-1], "missing from .checksums plugin_fingerprints") {
		t.Fatalf("missing-lock error not reported: %+v", r.Errors)
	}
}

func TestGenerateChecksumsWithPluginsDryRunNoFile(t *testing.T) {
	files, tmp := lockFixture(t)
	pluginsDir := filepath.Join(tmp, "plugins")
	man, entry := writePluginFixture(t, pluginsDir, "gmail", "name: gmail\n", "bin\n")

	err := GenerateChecksumsWithPlugins(files, []ResolvedPlugin{{
		Name:           "gmail",
		Enabled:        true,
		ManifestPath:   man,
		EntrypointPath: entry,
	}}, testFPNonce(), true)
	if err != nil {
		t.Fatalf("dry run errored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".checksums")); !os.IsNotExist(err) {
		t.Fatal(".checksums must not be written in dry-run")
	}
}
