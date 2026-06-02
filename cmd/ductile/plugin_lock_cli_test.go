package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
)

// stripVault removes the seeded vault + age key so the keyed-or-fail paths can be
// exercised (attestation must hard-fail without a loadable vault).
func stripVault(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"vault.age", "age.key"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			t.Fatalf("strip %s: %v", name, err)
		}
	}
}

// extractAllCode pulls the 5-char confirmation code out of a `plugin lock --all`
// preview ("  ductile plugin lock --all <code>").
func extractAllCode(t *testing.T, stdout string) string {
	t.Helper()
	const marker = "lock --all "
	idx := strings.LastIndex(stdout, marker)
	if idx < 0 {
		t.Fatalf("preview missing confirm command: %s", stdout)
	}
	code := strings.TrimSpace(stdout[idx+len(marker):])
	if i := strings.IndexAny(code, " \n"); i >= 0 {
		code = code[:i]
	}
	if len(code) != 5 {
		t.Fatalf("expected 5-char code, got %q from %s", code, stdout)
	}
	return code
}

// ISC-6: config lock's config-only path needs no vault — it computes nothing
// keyed, so it must succeed even with plugins configured and no vault present.
func TestConfigLockNeedsNoVaultEvenWithPlugins(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	stripVault(t, tmp)

	code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp})
	if code != 0 {
		t.Fatalf("config lock must not need a vault: code=%d stderr=%s", code, stderr)
	}
	m, err := config.LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if len(m.PluginFingerprints) != 0 {
		t.Fatalf("config lock must not attest plugins: %+v", m.PluginFingerprints)
	}
}

// ISC-15: plugin lock is keyed-or-fail — without a loadable vault it must hard
// fail, never silently fall through to an unkeyed digest.
func TestPluginLockFailsClosedWithoutVault(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp}); code != 0 {
		t.Fatalf("config lock failed: %s", stderr)
	}
	stripVault(t, tmp)

	code, _, stderr := captureRunPluginLock(t, []string{"--config-dir", tmp, "gmail"})
	if code == 0 {
		t.Fatal("plugin lock must fail closed without a vault")
	}
	if !strings.Contains(strings.ToLower(stderr), "vault") {
		t.Fatalf("error should mention the missing vault: %s", stderr)
	}
}

// ISC-A1: a routine config lock must NOT re-bless tampered plugin bytes. The
// recorded hash is preserved and verify still rejects the swap (Threat A closed).
func TestConfigLockDoesNotReblessTamperedBytes(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")

	before, err := config.LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	lockedHash := before.PluginFingerprints[0].EntrypointHash

	// Swap the entrypoint, then run config lock for an unrelated reason.
	if err := os.WriteFile(filepath.Join(tmp, "plugins", "gmail", "gmail"), []byte("#!/bin/sh\necho swapped\n"), 0755); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp}); code != 0 {
		t.Fatalf("config lock failed: %s", stderr)
	}

	after, err := config.LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums after: %v", err)
	}
	if after.PluginFingerprints[0].EntrypointHash != lockedHash {
		t.Fatal("config lock re-blessed swapped bytes — Threat A not closed")
	}
	if err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml")); err == nil {
		t.Fatal("verify must still reject the swapped plugin after a routine config lock")
	}
}

// ISC-17/18/19: --all without a code previews changed/new plugins, prints a
// confirm code, and writes nothing.
func TestPluginLockAllPreviewListsAndDoesNotWrite(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp}); code != 0 {
		t.Fatalf("config lock failed: %s", stderr)
	}

	code, stdout, stderr := captureRunPluginLock(t, []string{"--config-dir", tmp, "--all"})
	if code != 0 {
		t.Fatalf("preview failed: code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "gmail") {
		t.Fatalf("preview should list gmail as a change: %s", stdout)
	}
	_ = extractAllCode(t, stdout) // also asserts a 5-char code is present

	m, err := config.LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if len(m.PluginFingerprints) != 0 {
		t.Fatalf("preview must not write fingerprints: %+v", m.PluginFingerprints)
	}
}

// ISC-20: --all <matching-code> commits the attestation.
func TestPluginLockAllCommitsWithMatchingCode(t *testing.T) {
	tmp := buildAliasFixture(t)
	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp}); code != 0 {
		t.Fatalf("config lock failed: %s", stderr)
	}

	_, stdout, _ := captureRunPluginLock(t, []string{"--config-dir", tmp, "--all"})
	confirm := extractAllCode(t, stdout)

	if code, _, stderr := captureRunPluginLock(t, []string{"--config-dir", tmp, "--all", confirm}); code != 0 {
		t.Fatalf("commit with matching code failed: %s", stderr)
	}
	m, err := config.LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if len(m.PluginFingerprints) != 2 {
		t.Fatalf("commit should attest both gmail and gmail-work, got %d", len(m.PluginFingerprints))
	}
}

// ISC-21: a wrong code refuses and writes nothing.
func TestPluginLockAllWrongCodeRefuses(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp}); code != 0 {
		t.Fatalf("config lock failed: %s", stderr)
	}
	_, _, _ = captureRunPluginLock(t, []string{"--config-dir", tmp, "--all"}) // preview

	code, _, stderr := captureRunPluginLock(t, []string{"--config-dir", tmp, "--all", "ZZZZZ"})
	if code == 0 {
		t.Fatal("commit with a wrong code must be refused")
	}
	if !strings.Contains(stderr, "does not match") {
		t.Fatalf("error should explain the code mismatch: %s", stderr)
	}
	m, _ := config.LoadChecksums(tmp)
	if len(m.PluginFingerprints) != 0 {
		t.Fatalf("refused commit must not write fingerprints: %+v", m.PluginFingerprints)
	}
}

// ISC-A2: the code is bound to the proposed bytes, so changing a plugin between
// preview and commit invalidates the previewed code (TOCTOU guard).
func TestPluginLockAllCodeInvalidatesOnByteChange(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp}); code != 0 {
		t.Fatalf("config lock failed: %s", stderr)
	}
	_, stdout, _ := captureRunPluginLock(t, []string{"--config-dir", tmp, "--all"})
	stale := extractAllCode(t, stdout)

	// The world moves: the plugin is rebuilt after the operator previewed.
	if err := os.WriteFile(filepath.Join(tmp, "plugins", "gmail", "gmail"), []byte("#!/bin/sh\necho rebuilt\n"), 0755); err != nil {
		t.Fatalf("rebuild plugin: %v", err)
	}

	code, _, stderr := captureRunPluginLock(t, []string{"--config-dir", tmp, "--all", stale})
	if code == 0 {
		t.Fatal("a stale code must not commit bytes that changed since preview")
	}
	if !strings.Contains(stderr, "does not match") {
		t.Fatalf("error should explain the stale-code mismatch: %s", stderr)
	}
}

// Flags must be tolerated AFTER positional args (ductile is LLM-operated; an
// agent will not reliably put --config-dir before the plugin name). The stdlib
// flag package stops at the first non-flag, so this exercises the interspersed-
// flag handling.
func TestPluginLockFlagsAfterPositional(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmp}); code != 0 {
		t.Fatalf("config lock failed: %s", stderr)
	}

	// Single-name with the flag AFTER the name.
	if code, _, stderr := captureRunPluginLock(t, []string{"gmail", "--config-dir", tmp}); code != 0 {
		t.Fatalf("plugin lock <name> with trailing flag failed: %s", stderr)
	}
	m, err := config.LoadChecksums(tmp)
	if err != nil {
		t.Fatalf("LoadChecksums: %v", err)
	}
	if len(m.PluginFingerprints) != 1 {
		t.Fatalf("trailing-flag attest should have written 1 fingerprint, got %d", len(m.PluginFingerprints))
	}

	// --all preview with the flag AFTER the --all switch and a code positional.
	_, stdout, _ := captureRunPluginLock(t, []string{"--all", "--config-dir", tmp})
	if !strings.Contains(stdout, "Nothing to attest") && !strings.Contains(stdout, "lock --all ") {
		t.Fatalf("--all with trailing flag produced no usable output: %s", stdout)
	}
}

// ISC-22: --all with nothing to change is a no-op that writes nothing.
func TestPluginLockAllNoChangesIsNoOp(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	before, err := os.ReadFile(filepath.Join(tmp, ".checksums"))
	if err != nil {
		t.Fatalf("read checksums: %v", err)
	}

	code, stdout, stderr := captureRunPluginLock(t, []string{"--config-dir", tmp, "--all"})
	if code != 0 {
		t.Fatalf("--all no-op failed: %s", stderr)
	}
	if !strings.Contains(strings.ToLower(stdout), "nothing to attest") {
		t.Fatalf("expected nothing-to-attest message, got: %s", stdout)
	}
	after, err := os.ReadFile(filepath.Join(tmp, ".checksums"))
	if err != nil {
		t.Fatalf("read checksums after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("no-op --all must not rewrite .checksums")
	}
}
