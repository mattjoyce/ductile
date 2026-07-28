package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/configsnapshot"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/secrets"
	"github.com/mattjoyce/ductile/internal/vault"
)

// seedVault writes an age key to <configDir>/age.key and initializes a vault at
// <configDir>/vault.age (seeding the core fingerprint nonce). Keyed plugin
// attestation requires this nonce: lock and verify of configured plugins now
// fail closed without a loadable vault, so any happy-path lock/verify fixture
// must seed one. The default resolution paths (resolveKeyring →
// <configDir>/age.key, resolveVaultPath → <configDir>/vault.age) mean no config
// fields are needed to wire it up.
func seedVault(t *testing.T, configDir string) {
	seedVaultSecrets(t, configDir, nil)
}

// seedVaultSecrets writes a keyed vault (age.key + vault.age) into configDir
// holding the given secrets, so a Load() there resolves their secret_refs now that
// the vault is the sole secret source (epic #48). Pair it with a config secrets:
// block pointing age_key_file/vault_file at age.key/vault.age.
func seedVaultSecrets(t *testing.T, configDir string, kv map[string]string) {
	t.Helper()
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("generate age identity: %v", err)
	}
	keyPath := filepath.Join(configDir, "age.key")
	if err := os.WriteFile(keyPath, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write age key: %v", err)
	}
	kr, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}
	v, _, err := vault.Init(filepath.Join(configDir, "vault.age"), kr, time.Now())
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	for name, val := range kv {
		if err := v.Store().SetSecret(name, val, nil, vault.PatternManual, time.Now()); err != nil {
			t.Fatalf("seed secret %q: %v", name, err)
		}
	}
	if len(kv) > 0 {
		if err := v.Save(); err != nil {
			t.Fatalf("vault save: %v", err)
		}
	}
}

// TestFingerprintNonceForConfigReusesOwnerWithoutDiskLoad proves the #43
// single-decrypt path: when a non-nil owner is supplied (the vault already
// decrypted by config.LoadWithVault at boot/reload), fingerprintNonceForConfig
// sources the nonce from that in-memory snapshot and never touches disk. The
// configDir passed here holds NO vault, so a fallback to config.LoadVault would
// fail closed — success therefore proves the owner snapshot was reused.
func TestFingerprintNonceForConfigReusesOwnerWithoutDiskLoad(t *testing.T) {
	keyDir := t.TempDir()
	keyPath := filepath.Join(keyDir, "age.key")
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("generate age identity: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write age key: %v", err)
	}
	kr, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}
	owner, _, err := vault.Init(filepath.Join(keyDir, "vault.age"), kr, time.Now())
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	want, err := owner.FingerprintNonce()
	if err != nil {
		t.Fatalf("owner nonce: %v", err)
	}

	emptyDir := t.TempDir() // deliberately has no vault on disk
	got, err := fingerprintNonceForConfig(emptyDir, &config.Config{}, owner)
	if err != nil {
		t.Fatalf("owner path must not fail or fall back to disk: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("owner path did not reuse the in-memory owner snapshot nonce")
	}
}

// buildFingerprintFixture writes a minimal config directory with service.allow_symlinks=true
// (so macOS /var/folders/ → /private/var/folders/ does not trip the symlink refusal)
// and one configured plugin named "gmail" with its manifest + entrypoint.
// Returns the absolute configDir path.
func buildFingerprintFixture(t *testing.T, pluginEnabled bool) string {
	t.Helper()
	tmp := t.TempDir()

	configYAML := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
plugins:
  gmail:
    enabled: ` + boolStr(pluginEnabled) + `
`
	if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	pluginDir := filepath.Join(tmp, "plugins", "gmail")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: gmail
version: 0.1.0
protocol: 2
entrypoint: gmail
commands:
  - name: poll
    type: write
`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "gmail"), []byte("#!/bin/sh\necho gmail\n"), 0755); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}
	// Keyed attestation needs the vault nonce for lock + verify of configured
	// plugins. Seed a loadable vault so the happy-path wiring tests succeed.
	seedVault(t, tmp)
	return tmp
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// buildAliasFixture extends the gmail fixture with a gmail-work alias (uses: gmail),
// so attestation of an alias pair can be exercised end to end.
func buildAliasFixture(t *testing.T) string {
	t.Helper()
	tmp := buildFingerprintFixture(t, true)
	aliasConfig := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
plugins:
  gmail:
    enabled: true
  gmail-work:
    enabled: true
    uses: gmail
`
	if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(aliasConfig), 0644); err != nil {
		t.Fatalf("rewrite config.yaml: %v", err)
	}
	return tmp
}

func TestResolveConfiguredPluginFingerprintsHappyPath(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	configPath := filepath.Join(tmp, "config.yaml")

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	resolved, err := resolveConfiguredPluginFingerprints(cfg, configPath)
	if err != nil {
		t.Fatalf("resolveConfiguredPluginFingerprints: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("want 1 resolved plugin, got %d", len(resolved))
	}
	if resolved[0].Name != "gmail" || !resolved[0].Enabled {
		t.Fatalf("wrong resolved entry: %+v", resolved[0])
	}
	if !filepath.IsAbs(resolved[0].ManifestPath) || !filepath.IsAbs(resolved[0].EntrypointPath) {
		t.Fatalf("resolved paths must be absolute: %+v", resolved[0])
	}
}

func TestResolveConfiguredPluginFingerprintsMissingPluginErrors(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	configPath := filepath.Join(tmp, "config.yaml")

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	// Configure a plugin that doesn't exist on disk.
	cfg.Plugins["ghost"] = config.PluginConf{Enabled: true}

	_, err = resolveConfiguredPluginFingerprints(cfg, configPath)
	if err == nil {
		t.Fatal("expected error for configured-but-missing plugin")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error should name the missing plugin: %v", err)
	}
}

func TestResolveConfiguredPluginFingerprintsDisabledStillIncluded(t *testing.T) {
	tmp := buildFingerprintFixture(t, false)
	configPath := filepath.Join(tmp, "config.yaml")

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	resolved, err := resolveConfiguredPluginFingerprints(cfg, configPath)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Enabled {
		t.Fatalf("disabled plugin should still appear with Enabled=false: %+v", resolved)
	}
}

// §3.1: a routine `config lock` no longer attests plugins. It writes config-file
// hashes only and never re-hashes plugin bytes (closing Threat A — a lock done for
// an unrelated reason cannot bless a swapped binary).
func TestConfigLockDoesNotAttestPlugins(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)

	code, stdout, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp, "-v"})
	if code != 0 {
		t.Fatalf("runConfigHashUpdate code=%d stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "DISCOVER [plugin]") {
		t.Fatalf("config lock must not emit plugin-blessing discovery lines: %s", stdout)
	}

	m, err := config.LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if len(m.PluginFingerprints) != 0 {
		t.Fatalf("config lock must not write plugin fingerprints, got %d", len(m.PluginFingerprints))
	}
}

// `ductile plugin lock <name>` is the explicit, per-plugin attestation act.
func TestPluginLockSingleWritesFingerprint(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)

	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp}); code != 0 {
		t.Fatalf("config lock failed: %s", stderr)
	}
	if code, _, stderr := captureRunPluginLock(t, []string{"--config-dir", tmp, "gmail"}); code != 0 {
		t.Fatalf("plugin lock gmail failed: %s", stderr)
	}

	m, err := config.LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if len(m.PluginFingerprints) != 1 || m.PluginFingerprints[0].Name != "gmail" {
		t.Fatalf("want 1 fingerprint named gmail, got %+v", m.PluginFingerprints)
	}
	if m.PluginFingerprints[0].EntrypointHash == "" {
		t.Fatalf("attested fingerprint must carry an entrypoint hash: %+v", m.PluginFingerprints[0])
	}
}

// plugin lock <name> must leave every OTHER plugin's recorded entry untouched
// (ISC-A3): re-attesting A cannot sweep in a swapped B.
func TestPluginLockSingleLeavesOthersUntouched(t *testing.T) {
	tmp := buildAliasFixture(t)
	lockConfigAndPlugins(t, tmp, "gmail", "gmail-work")

	before, err := config.LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	var workBefore config.PluginFingerprint
	for _, fp := range before.PluginFingerprints {
		if fp.Name == "gmail-work" {
			workBefore = fp
		}
	}

	// Re-attest only gmail; gmail-work's entry must be byte-identical afterwards.
	if code, _, stderr := captureRunPluginLock(t, []string{"--config-dir", tmp, "gmail"}); code != 0 {
		t.Fatalf("re-lock gmail failed: %s", stderr)
	}
	after, err := config.LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums after: %v", err)
	}
	var workAfter config.PluginFingerprint
	for _, fp := range after.PluginFingerprints {
		if fp.Name == "gmail-work" {
			workAfter = fp
		}
	}
	if workAfter != workBefore {
		t.Fatalf("re-attesting gmail must not touch gmail-work: before=%+v after=%+v", workBefore, workAfter)
	}
}

// plugin lock <name> errors when the plugin is not configured.
func TestPluginLockUnconfiguredErrors(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp}); code != 0 {
		t.Fatalf("config lock failed: %s", stderr)
	}
	code, _, stderr := captureRunPluginLock(t, []string{"--config-dir", tmp, "ghost"})
	if code == 0 {
		t.Fatal("expected error attesting an unconfigured plugin")
	}
	if !strings.Contains(stderr, "ghost") || !strings.Contains(stderr, "not configured") {
		t.Fatalf("error should name the unconfigured plugin: %s", stderr)
	}
}

// plugin lock <name> errors when the plugin is configured but not on disk.
func TestPluginLockNotDiscoverableErrors(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp}); code != 0 {
		t.Fatalf("config lock failed: %s", stderr)
	}
	if err := os.RemoveAll(filepath.Join(tmp, "plugins", "gmail")); err != nil {
		t.Fatalf("remove plugin files: %v", err)
	}
	code, _, stderr := captureRunPluginLock(t, []string{"--config-dir", tmp, "gmail"})
	if code == 0 {
		t.Fatal("expected error attesting a configured-but-missing plugin")
	}
	if !strings.Contains(stderr, "gmail") {
		t.Fatalf("error should name the missing plugin: %s", stderr)
	}
}

func TestRunConfigHashUpdateNoConfiguredPluginsOmitsFingerprints(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "plugins"), 0755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	configYAML := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
plugins: {}
`
	if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp})
	if code != 0 {
		t.Fatalf("runConfigHashUpdate code=%d stderr=%s", code, stderr)
	}

	m, err := config.LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if len(m.PluginFingerprints) != 0 {
		t.Fatalf("no configured plugins should not emit fingerprints, got %+v", m.PluginFingerprints)
	}
}

func TestVerifyPluginFingerprintsForConfigHappyPath(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	// Lock first, including plugins.
	lockConfigAndPlugins(t, tmp, "gmail")

	if err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil); err != nil {
		t.Fatalf("verify should pass on unchanged bytes: %v", err)
	}
}

func TestLoadPluginFingerprintRecordsDisabledMissingPlugin(t *testing.T) {
	tmp := buildFingerprintFixture(t, false)
	lockConfigAndPlugins(t, tmp, "gmail")
	if err := os.RemoveAll(filepath.Join(tmp, "plugins", "gmail")); err != nil {
		t.Fatalf("remove plugin files: %v", err)
	}

	cfg, err := config.Load(filepath.Join(tmp, "config.yaml"))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	registry := plugin.NewRegistry()

	records := loadPluginFingerprintRecords(filepath.Join(tmp, "config.yaml"), cfg, registry)
	if len(records) != 1 {
		t.Fatalf("expected one plugin fingerprint record, got %+v", records)
	}
	record := records[0]
	if record.Plugin != "gmail" {
		t.Fatalf("record plugin = %q, want gmail", record.Plugin)
	}
	if record.Enabled {
		t.Fatal("disabled plugin was recorded as enabled")
	}
	if record.Available {
		t.Fatal("missing plugin files should be recorded as unavailable")
	}
	if !strings.Contains(record.UnavailableReason, "not discovered") {
		t.Fatalf("unexpected unavailable reason: %q", record.UnavailableReason)
	}
	if record.ManifestHash == "" || record.EntrypointHash == "" {
		t.Fatalf("locked hashes should be retained for unavailable disabled plugin: %+v", record)
	}

	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("marshal records: %v", err)
	}
	if !strings.Contains(string(raw), `"available":false`) {
		t.Fatalf("snapshot JSON does not mark unavailable plugin: %s", raw)
	}
	var decoded []configsnapshot.PluginFingerprintRecord
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal records: %v", err)
	}
}

func TestVerifyPluginFingerprintsForConfigEntrypointTamperFails(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	// Tamper with entrypoint.
	entryPath := filepath.Join(tmp, "plugins", "gmail", "gmail")
	if err := os.WriteFile(entryPath, []byte("#!/bin/sh\necho tampered\n"), 0755); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil)
	if err == nil {
		t.Fatal("expected error after entrypoint tampered")
	}
	if !strings.Contains(err.Error(), "gmail") || !strings.Contains(err.Error(), "entrypoint") {
		t.Fatalf("error should name plugin and entrypoint kind: %v", err)
	}
	if !strings.Contains(err.Error(), "ductile plugin lock") {
		t.Fatalf("error should include recovery command: %v", err)
	}
}

func TestVerifyPluginFingerprintsForConfigNoChecksumsIsNoOp(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	// No lock at all.
	if err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil); err != nil {
		t.Fatalf("verify should no-op when .checksums absent: %v", err)
	}
}

func TestVerifyPluginFingerprintsForConfigNoPluginSectionWithConfiguredPluginsFails(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	files, err := config.DiscoverConfigFiles(tmp)
	if err != nil {
		t.Fatalf("DiscoverConfigFiles: %v", err)
	}
	if err := config.GenerateChecksumsFromDiscovery(files, false); err != nil {
		t.Fatalf("legacy lock failed: %v", err)
	}
	err = verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil)
	if err == nil {
		t.Fatal("expected missing plugin_fingerprints to fail when plugins are configured")
	}
	if !strings.Contains(err.Error(), "plugin fingerprints missing") || !strings.Contains(err.Error(), "ductile plugin lock") {
		t.Fatalf("error should tell operator to relock: %v", err)
	}
}

func TestVerifyPluginFingerprintsForConfigNoPluginSectionWithoutConfiguredPluginsIsNoOp(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "plugins"), 0755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	configYAML := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
plugins: {}
`
	if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	files, err := config.DiscoverConfigFiles(tmp)
	if err != nil {
		t.Fatalf("DiscoverConfigFiles: %v", err)
	}
	if err := config.GenerateChecksumsFromDiscovery(files, false); err != nil {
		t.Fatalf("legacy lock failed: %v", err)
	}
	if err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil); err != nil {
		t.Fatalf("no configured plugins should allow missing plugin_fingerprints: %v", err)
	}
}

// TestVerifyPluginFingerprintsForConfigManifestTamperFails parallels the
// entrypoint-tamper case but flips the manifest bytes, exercising the
// ManifestHash-mismatch branch of VerifyPluginFingerprints end-to-end.
func TestVerifyPluginFingerprintsForConfigManifestTamperFails(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	manPath := filepath.Join(tmp, "plugins", "gmail", "manifest.yaml")
	tampered := `manifest_spec: ductile.plugin
manifest_version: 1
name: gmail
version: 9.9.9
protocol: 2
entrypoint: gmail
commands:
  - name: poll
    type: write
`
	if err := os.WriteFile(manPath, []byte(tampered), 0644); err != nil {
		t.Fatalf("tamper manifest: %v", err)
	}

	err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil)
	if err == nil {
		t.Fatal("expected error after manifest tamper")
	}
	if !strings.Contains(err.Error(), "gmail") || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("error should name plugin and manifest kind: %v", err)
	}
	if !strings.Contains(err.Error(), "ductile plugin lock") {
		t.Fatalf("error should include recovery command: %v", err)
	}
}

// TestRunConfigHashUpdateDefaultLockEmbedsAlias exercises the alias path end
// to end: a second plugin entry with `uses: gmail` must share the base's
// paths and hashes, and carry Uses in the manifest.
func TestPluginLockEmbedsAlias(t *testing.T) {
	tmp := buildAliasFixture(t)

	lockConfigAndPlugins(t, tmp, "gmail", "gmail-work")
	m, err := config.LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if len(m.PluginFingerprints) != 2 {
		t.Fatalf("want 2 fingerprints (base + alias), got %d", len(m.PluginFingerprints))
	}
	// Sorted by name: gmail, gmail-work
	base := m.PluginFingerprints[0]
	alias := m.PluginFingerprints[1]
	if alias.Name != "gmail-work" || alias.Uses != "gmail" {
		t.Fatalf("alias not recorded with Uses: %+v", alias)
	}
	if alias.ManifestHash != base.ManifestHash || alias.EntrypointHash != base.EntrypointHash {
		t.Fatalf("alias should share base hashes: base=%+v alias=%+v", base, alias)
	}

	// Now verify: no tampering, should pass cleanly.
	if err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil); err != nil {
		t.Fatalf("verify should pass for locked alias pair: %v", err)
	}
}

// TestVerifyPluginFingerprintsForConfigDisabledTamperIsNotFatal confirms
// that tampering a disabled plugin yields warnings-only (non-fatal) so
// reload does NOT reject the config.
func TestVerifyPluginFingerprintsForConfigDisabledTamperIsNotFatal(t *testing.T) {
	tmp := buildFingerprintFixture(t, false) // disabled
	lockConfigAndPlugins(t, tmp, "gmail")
	// Tamper disabled plugin's entrypoint.
	if err := os.WriteFile(filepath.Join(tmp, "plugins", "gmail", "gmail"), []byte("rebuilt\n"), 0755); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	if err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil); err != nil {
		t.Fatalf("disabled plugin tamper must not fail verify (warn-only): %v", err)
	}
}

func TestVerifyPluginFingerprintsForConfigEnabledAfterDisabledLockTamperFails(t *testing.T) {
	tmp := buildFingerprintFixture(t, false)
	lockConfigAndPlugins(t, tmp, "gmail")
	enabledConfig := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
plugins:
  gmail:
    enabled: true
`
	if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(enabledConfig), 0644); err != nil {
		t.Fatalf("enable plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "plugins", "gmail", "gmail"), []byte("rebuilt\n"), 0755); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil)
	if err == nil {
		t.Fatal("expected current enabled plugin tamper to fail verify")
	}
	if !strings.Contains(err.Error(), "entrypoint") {
		t.Fatalf("error should mention entrypoint mismatch: %v", err)
	}
}

func TestVerifyPluginFingerprintsForConfigConfiguredMissingPluginFails(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	if err := os.RemoveAll(filepath.Join(tmp, "plugins", "gmail")); err != nil {
		t.Fatalf("remove plugin: %v", err)
	}

	err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil)
	if err == nil {
		t.Fatal("expected configured missing plugin to fail verify")
	}
	if !strings.Contains(err.Error(), "configured but was not discovered") {
		t.Fatalf("error should distinguish missing configured plugin: %v", err)
	}
}

// TestRunConfigHashUpdatePluginsDryRunLeavesChecksumsUntouched verifies
// dry-run hashes everything (still errors on missing plugin
// etc.) but never writes .checksums, so operators can sanity-check before
// committing.
func TestRunConfigHashUpdatePluginsDryRunLeavesChecksumsUntouched(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	// Seed an existing .checksums so we can confirm dry-run does NOT overwrite it.
	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp}); code != 0 {
		t.Fatalf("seed lock failed: %s", stderr)
	}
	before, err := os.ReadFile(filepath.Join(tmp, ".checksums"))
	if err != nil {
		t.Fatalf("read seed checksums: %v", err)
	}

	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp, "--dry-run"}); code != 0 {
		t.Fatalf("dry-run lock failed: %s", stderr)
	}

	after, err := os.ReadFile(filepath.Join(tmp, ".checksums"))
	if err != nil {
		t.Fatalf("read checksums after dry-run: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("dry-run must not modify .checksums\nbefore=%s\nafter=%s", before, after)
	}
}

// TestVerifyPluginFingerprintsForConfigStaleRecordNotFatal captures the
// case where the operator locked a plugin, then removed it from config.yaml
// without re-locking. Classification is warning-only, so verify must not
// reject the reload.
func TestVerifyPluginFingerprintsForConfigStaleRecordNotFatal(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")

	// Rewrite config.yaml removing gmail entirely (but keeping plugin files).
	cleaned := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
plugins: {}
`
	if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(cleaned), 0644); err != nil {
		t.Fatalf("rewrite config.yaml: %v", err)
	}
	if err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil); err != nil {
		t.Fatalf("stale fingerprint record must be warn-only, got error: %v", err)
	}
}

// TestPluginLockReattestsModifiedBytesCleanly verifies that re-attesting a
// plugin via `plugin lock <name>` after its bytes change records the NEW hash
// and overwrites .checksums atomically with no stray temp artifacts.
func TestPluginLockReattestsModifiedBytesCleanly(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	first, _ := config.LoadChecksums(tmp)

	// Modify the plugin entrypoint between attestations.
	if err := os.WriteFile(filepath.Join(tmp, "plugins", "gmail", "gmail"), []byte("v2\n"), 0755); err != nil {
		t.Fatalf("modify entrypoint: %v", err)
	}

	if code, _, stderr := captureRunPluginLock(t, []string{"--config-dir", tmp, "gmail"}); code != 0 {
		t.Fatalf("re-attest failed: %s", stderr)
	}
	second, _ := config.LoadChecksums(tmp)

	if first.PluginFingerprints[0].EntrypointHash == second.PluginFingerprints[0].EntrypointHash {
		t.Fatal("relock should produce a new entrypoint hash for modified bytes")
	}

	// No .checksums.tmp-* artifacts should remain after the two locks.
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".checksums.tmp-") {
			t.Fatalf("stray atomic-write temp file after relock: %s", e.Name())
		}
	}
}

// buildBootVerifyFixture is buildFingerprintFixture plus an admission block that
// enables verify_integrity_on_boot, so buildRuntime actually runs the boot
// integrity-verification path (and thus the #43 owner-threaded fingerprint
// verify). The plugin + vault seeded by buildFingerprintFixture are retained.
func buildBootVerifyFixture(t *testing.T) string {
	t.Helper()
	tmp := buildFingerprintFixture(t, true)
	cfg := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
  admission:
    verify_integrity_on_boot: true
plugins:
  gmail:
    enabled: true
`
	if err := os.WriteFile(filepath.Join(tmp, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatalf("rewrite config.yaml with admission block: %v", err)
	}
	return tmp
}

// TestVerifyReloadIntegrityWithOwnerAcceptsCleanBytes covers the #43 happy path
// that no prior test exercised: verifyReloadIntegrity called with a NON-nil vault
// owner (the snapshot config.LoadWithVault decrypts at boot/reload). The nonce is
// sourced from that owner; on unchanged bytes the keyed fingerprint verify passes.
func TestVerifyReloadIntegrityWithOwnerAcceptsCleanBytes(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	configPath := filepath.Join(tmp, "config.yaml")

	_, owner, err := config.LoadWithVault(configPath)
	if err != nil {
		t.Fatalf("LoadWithVault: %v", err)
	}
	if owner == nil {
		t.Fatal("expected a non-nil vault owner from LoadWithVault")
	}
	if err := verifyReloadIntegrity(configPath, false, owner); err != nil {
		t.Fatalf("owner-nonce verify should pass on clean bytes: %v", err)
	}
}

// TestVerifyReloadIntegrityWithOwnerRejectsTamper proves the owner-threaded
// verify stays fail-closed: a plugin entrypoint tampered after attestation is
// rejected even though the nonce now comes from the in-memory owner rather than a
// fresh decrypt (#43 must not weaken attestation). Codifies Dell scenario C.
func TestVerifyReloadIntegrityWithOwnerRejectsTamper(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	configPath := filepath.Join(tmp, "config.yaml")

	_, owner, err := config.LoadWithVault(configPath)
	if err != nil {
		t.Fatalf("LoadWithVault: %v", err)
	}
	if owner == nil {
		t.Fatal("expected a non-nil vault owner from LoadWithVault")
	}

	entry := filepath.Join(tmp, "plugins", "gmail", "gmail")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\necho tampered\n"), 0755); err != nil {
		t.Fatalf("tamper entrypoint: %v", err)
	}

	err = verifyReloadIntegrity(configPath, false, owner)
	if err == nil {
		t.Fatal("owner-nonce verify must reject a tampered attested entrypoint")
	}
	if !strings.Contains(err.Error(), "entrypoint") {
		t.Fatalf("error should cite the entrypoint mismatch: %v", err)
	}
}

// TestBuildRuntimeBootVerifyRejectsTamperWithOwner covers the boot wiring that
// had no automated test: with verify_integrity_on_boot enabled, buildRuntime must
// thread its opts.vaultOwner into the boot integrity check and fail closed when an
// attested plugin is swapped. The integrity check runs before any DB/listener
// setup, so buildRuntime returns the error early (no runtime to stop).
func TestBuildRuntimeBootVerifyRejectsTamperWithOwner(t *testing.T) {
	tmp := buildBootVerifyFixture(t)
	lockConfigAndPlugins(t, tmp, "gmail")
	configPath := filepath.Join(tmp, "config.yaml")

	cfg, owner, err := config.LoadWithVault(configPath)
	if err != nil {
		t.Fatalf("LoadWithVault: %v", err)
	}
	if owner == nil {
		t.Fatal("expected a non-nil vault owner from LoadWithVault")
	}
	if !cfg.Service.AdmissionPolicy().VerifyIntegrityOnBoot {
		t.Fatal("fixture must enable verify_integrity_on_boot to exercise the boot-verify path")
	}

	// Swap the attested entrypoint after locking.
	entry := filepath.Join(tmp, "plugins", "gmail", "gmail")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\necho tampered\n"), 0755); err != nil {
		t.Fatalf("tamper entrypoint: %v", err)
	}

	rt, err := buildRuntime(cfg, configPath, "test", nil, make(chan error, 4), runtimeBuildOptions{vaultOwner: owner})
	if rt != nil {
		rt.Stop()
	}
	if err == nil {
		t.Fatal("boot must fail closed when an attested plugin is tampered (verify_integrity_on_boot + owner)")
	}
	if !strings.Contains(err.Error(), "integrity check failed") && !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("boot error should cite an integrity/fingerprint failure, got: %v", err)
	}
}

// #173: an unusable manifest and an absent one are different verdicts, and the
// config snapshot must not blur them. Absent means integrity was never enabled,
// so an empty plugin table is honest. Unusable means attestation is enabled and
// cannot be evaluated — a box in that state must not produce a snapshot that
// reads like a clean one.
//
// The manifest is broken by version rather than by mode bits deliberately: the
// privileged CI gate runs this package as root, and root reads a 0000 file
// happily, so a chmod-based fixture would prove nothing there.
func TestLoadPluginFingerprintRecordsUnusableManifest(t *testing.T) {
	tests := []struct {
		name           string
		breakChecksums func(t *testing.T, path string)
		wantRecords    int
		wantReason     string
	}{
		{
			name: "unsupported version is unusable, not absent",
			breakChecksums: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("version: 99\nhashes: {}\n"), 0o600); err != nil {
					t.Fatalf("rewrite checksums: %v", err)
				}
			},
			wantRecords: 1,
			wantReason:  ".checksums could not be read",
		},
		{
			name: "unparseable manifest is unusable, not absent",
			breakChecksums: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("{not-yaml\n"), 0o600); err != nil {
					t.Fatalf("rewrite checksums: %v", err)
				}
			},
			wantRecords: 1,
			wantReason:  ".checksums could not be read",
		},
		{
			name: "absent manifest records nothing",
			breakChecksums: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove checksums: %v", err)
				}
			},
			wantRecords: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := buildFingerprintFixture(t, true)
			lockConfigAndPlugins(t, tmp, "gmail")
			tc.breakChecksums(t, filepath.Join(tmp, ".checksums"))

			cfg, err := config.Load(filepath.Join(tmp, "config.yaml"))
			if err != nil {
				t.Fatalf("config.Load: %v", err)
			}

			records := loadPluginFingerprintRecords(filepath.Join(tmp, "config.yaml"), cfg, plugin.NewRegistry())
			if len(records) != tc.wantRecords {
				t.Fatalf("got %d records, want %d: %+v", len(records), tc.wantRecords, records)
			}
			if tc.wantRecords == 0 {
				return
			}
			record := records[0]
			if record.Plugin != "gmail" {
				t.Errorf("record plugin = %q, want gmail", record.Plugin)
			}
			if record.Available {
				t.Error("plugin reported available while attestation was unevaluable")
			}
			if !strings.Contains(record.UnavailableReason, tc.wantReason) {
				t.Errorf("UnavailableReason = %q, want it to cite %q", record.UnavailableReason, tc.wantReason)
			}
			if record.ManifestHash != "" || record.EntrypointHash != "" {
				t.Errorf("unevaluable attestation leaked hashes: %+v", record)
			}
		})
	}
}
